# RESP — wire protocol

Package: `resp/` — files: `resp.go`, `reader.go`, `writer.go`, `resp_test.go`

## Why it exists

`server/` needs a wire format redis-cli and every other Redis client already speaks, so HCDB can
be driven with off-the-shelf tooling instead of a bespoke protocol. RESP (REdis Serialization
Protocol) is that format: five type tags, each one a byte followed by `\r\n`-terminated text,
with bulk data carried by an explicit length prefix rather than a delimiter. This package is
purely the wire format — parsing (`reader.go`) and serializing (`writer.go`) one `Value`
(`resp.go`) — and knows nothing about commands, `db.DB`, or networking; `server/` is what maps
parsed values to database calls (see [server.md](server.md)).

## The five types (`resp.go`)

```go
type Type byte

const (
    SimpleString Type = '+'
    Error        Type = '-'
    Integer      Type = ':'
    BulkString   Type = '$'
    Array        Type = '*'
)
```

Every RESP value on the wire starts with one of these bytes. Byte-level examples:

```
+OK\r\n              SimpleString "OK"
-ERR bad thing\r\n    Error "ERR bad thing"
:1000\r\n             Integer 1000
$6\r\nfoobar\r\n       BulkString "foobar" (6 bytes, then the payload, then \r\n)
$-1\r\n                BulkString, Null (no payload at all)
*2\r\n$3\r\nfoo\r\n$3\r\nbar\r\n     Array of 2 bulk strings: "foo", "bar"
*-1\r\n                Array, Null
```

`SimpleString` and `Error` carry plain text with no embedded `\r\n` and no length prefix — the
line itself *is* the delimiter, which is safe only because neither is meant to carry arbitrary
data (a status word or an error message, not a stored value). `BulkString` and `Array` are
length-prefixed instead, because they *do* need to carry arbitrary bytes (see below).

## `Value` — one parsed or to-be-written RESP value (`resp.go`)

```go
type Value struct {
    Type  Type
    Str   string
    Num   int64
    Elems []Value
    Null  bool // true for a null bulk string ($-1) or null array (*-1)
}
```

Which fields are meaningful depends on `Type`: `Str` for `SimpleString`/`Error`/`BulkString`,
`Num` for `Integer`, `Elems` for `Array`. Constructors (`SimpleStringValue`, `ErrorValue`,
`IntegerValue`, `BulkStringValue`, `NullBulkStringValue`, `ArrayValue`, `NullArrayValue`) build a
`Value` of the right shape without callers juggling the zero-value fields by hand.

`Null` is a **separate flag**, not overloaded onto an existing field (an empty string, or a `nil`
`Elems`), because those two things mean genuinely different things on the wire: `$0\r\n\r\n` is a
zero-length bulk string that exists — `Str == ""`, `Null == false` — while `$-1\r\n` says the
value doesn't exist at all — `Null == true`. A `GET` on a missing key must produce the second one
(`resp.NullBulkStringValue()`, see `server/dispatch.go`'s `doGet`); collapsing the two would make
"key absent" indistinguishable from "key holds the empty string".

## Reading (`reader.go`)

```go
func Read(r *bufio.Reader) (Value, error)
```

`Read` looks at the first byte of a line to pick a type, then parses that type's payload,
recursing into `Read` once per element for an `Array`. Worked example — a client issuing
`SET port 8080` puts these bytes on the wire:

```
*3\r\n$3\r\nSET\r\n$4\r\nport\r\n$4\r\n8080\r\n

*3          array, 3 elements follow
$3  SET     bulk string, 3 bytes
$4  port    bulk string, 4 bytes
$4  8080    bulk string, 4 bytes
```

Note `$4` for `8080`: the length counts **characters**, not magnitude — the value is the four
characters `'8','0','8','0'`, not the number 8080. A command's arguments carry no numeric type at
all; every one of them is a bulk string, because a key or a value may be arbitrary bytes of
arbitrary length. The `:` `Integer` type only ever appears in *replies* (e.g. `DEL`'s count) —
see `Write` below and `server/dispatch.go`.

### Why bulk strings are length-prefixed, not delimiter-scanned

A naive reader could look for the next `\r\n` to find where a string ends. `Read` never does
that for `BulkString`/`Array` payloads — it reads a decimal length, then does exactly
`io.ReadFull` of that many bytes plus the trailing `\r\n`:

```go
n, _ := strconv.Atoi(line[1:])
buf := make([]byte, n+2)          // +2 for the trailing \r\n
io.ReadFull(r, buf)
return Value{Type: BulkString, Str: string(buf[:n])}, nil
```

This is what makes the format **binary-safe**: a value HCDB stores can itself contain the bytes
`\r\n` (any key or value can, since HCDB has no notion of "text" versus "binary" data). A
delimiter-scanning reader would stop at the first embedded `\r\n` and truncate the value.
`TestReadBulkStringWithEmbeddedCRLF` is the regression test that exists specifically to prove
this: reading `$6\r\nab\r\ncd\r\n` must yield the full six-byte string `"ab\r\ncd"`, not `"ab"`.

### Chunked-read guarantee

`Read` is built entirely on `bufio.Reader`'s `ReadString`/`ReadByte`/`Read` methods, which block
and retry against the underlying connection as needed. Nothing in `Read` assumes a full line or a
full bulk-string payload has already arrived in one chunk — so a client that writes one byte at a
time is parsed identically to one that writes an entire command in a single syscall. This is an
explicitly tested guarantee, not an incidental property:

- `TestChunkedRead` feeds `Read` a fake `io.Reader` (`oneByteAtATime`) that returns exactly one
  byte per call, regardless of how large a buffer it's asked to fill.
- `TestChunkedReadOverRealConn` repeats the same case over an actual TCP socket, with the sender
  side pacing writes to one byte every millisecond via `conn.Write([]byte{b})` — so the chunking
  is enforced by the real OS/network stack, not just a test double standing in for one. Both
  assert the parsed command comes out identical to the un-chunked case.

This matters for a real server: a slow or adversarial client, or ordinary TCP segmentation, can
deliver a command's bytes in an arbitrary number of pieces, and `server.handleConn`'s call to
`resp.Read` has to produce the same `Value` regardless.

## Writing (`writer.go`)

```go
func Write(w *bufio.Writer, v Value) error
```

Serializes `v` onto `w` and flushes — recursing for `Array` elements, so a multi-element reply is
buffered as one write, not one syscall per element. Worked examples, replies to a few commands on
key `"city"` holding `"pune"`:

```
GET city      BulkStringValue("pune")   -> "$4\r\npune\r\n"
GET nosuchkey NullBulkStringValue()     -> "$-1\r\n"
SET city pune SimpleStringValue("OK")   -> "+OK\r\n"
DEL city      IntegerValue(1)           -> ":1\r\n"
bad command   ErrorValue("ERR ...")     -> "-ERR ...\r\n"
SCAN a z      ArrayValue(k, v)          -> "*2\r\n$4\r\ncity\r\n$4\r\npune\r\n"
```

`GET` and `DEL` both "return a number-ish thing" but use different types, and the reason is the
rule for the whole protocol: `GET` returns whatever bytes the user actually stored — arbitrary
length, possibly binary, possibly simply absent — so it must be a bulk string; `DEL` returns a
count the server itself computed, which is always a plain number, so it's an `Integer`. **User
bytes coming back out means bulk string; a server-computed fact means Integer.** Commands
arriving the other way (client → server) may only ever be arrays of bulk strings — see `Read`
above — replies are the only place all five types appear.

## Round-trip testing

`TestWriteRoundTrip` (`resp_test.go`) writes every representative `Value` (each type, `Null`
variants, nested/empty arrays) with `Write` and reads it back with `Read`, asserting structural
equality — the two halves of this package are tested together as inverses of each other, not
just independently.

## Known limitations

- **No streaming/incremental parse API.** `Read` returns only once a complete value (recursively,
  for arrays) has arrived; there's no way to parse a partial command and resume later without
  blocking the calling goroutine on I/O in the meantime. Acceptable for HCDB's one-goroutine-per-
  connection model (see [server.md](server.md)), where blocking is the point.
- **No inline-command support.** Real Redis also accepts a plain text line (no `*`/`$` framing)
  as a command for interactive use; this package only implements the typed protocol.
- **Errors carry no distinct error "kind" prefix** (real Redis errors conventionally start with a
  code word like `ERR` or `WRONGTYPE`) — `resp.Value` just stores the whole error string; any
  such convention is left to callers (see `server/dispatch.go`, which does prefix with `"ERR "`).

## Related

- [server.md](server.md) — the only consumer; maps parsed commands to `db.DB` calls
- [deployment.md](deployment.md) — how the RESP-speaking server gets built and run
