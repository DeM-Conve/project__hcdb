# Server — RESP-speaking TCP front end

Package: `server/` — files: `types.go`, `dispatch.go`, `server.go`

## Why it exists

`db.DB` is a Go library, callable only from a process linked against it. `server` puts a network
front end on top: it accepts TCP connections, parses commands off the wire with `resp.Read` (see
[resp.md](resp.md)), maps each one to a `db.DB` call, and writes a RESP reply back. That's what
lets `redis-cli`, or any other RESP client, talk to hcdb directly — see
[deployment.md](deployment.md) for the binary that wires this together and runs it.

## `Server` (`server.go`)

```go
type Server struct {
    database *db.DB
    listener atomic.Pointer[net.Listener]

    wg sync.WaitGroup

    connsMu sync.Mutex
    conns   map[net.Conn]struct{} // live connections, so shutdown can close idle ones
}

func New(database *db.DB) *Server {
    return &Server{database: database, conns: make(map[net.Conn]struct{})}
}
```

One `Server` wraps one already-open `*db.DB` — the caller (`cmd/hcdb-server/main.go`) opens it
once, and every accepted connection dispatches against that same shared instance, same as any
other caller of the `db` package. `Server` itself holds no storage-engine state; it's purely
connection bookkeeping.

Field shapes, and why each is what it is:

- **`listener atomic.Pointer[net.Listener]`**, not a plain `net.Listener` field. `Serve` creates
  the listener only once it's called (it needs `addr`, which isn't known at `New` time), from a
  different goroutine than whatever might call `Addr()` concurrently (tests binding to `:0` need
  the OS-assigned port before they can connect). An atomic pointer lets `Addr()` read it safely
  without a mutex, and lets `Serve` publish it exactly once with a single `Store`.
- **`connsMu sync.Mutex` + `conns map[net.Conn]struct{}`** — every live connection is tracked in a
  plain map guarded by a plain mutex. This isn't a performance-critical path (connections come
  and go far less often than commands are dispatched on any one of them), so the straightforward
  map+mutex is preferable to anything fancier. It exists for exactly one reason: graceful
  shutdown needs to reach connections that are otherwise invisible to it (see below).
- **`wg sync.WaitGroup`** — one `Add(1)`/`Done()` pair per accepted connection's goroutine.
  `Serve` doesn't return until every connection goroutine has actually exited, not just until
  they've been asked to — `wg.Wait()` is the mechanism for that.

## Accept loop and the goroutine-per-connection model

```go
func (s *Server) Serve(ctx context.Context, addr string) error {
    ln, _ := net.Listen("tcp", addr)
    s.listener.Store(&ln)

    go func() {
        <-ctx.Done()
        ln.Close()
        s.closeAllConns()
    }()

    for {
        conn, err := ln.Accept()
        if err != nil {
            if errors.Is(err, net.ErrClosed) {
                s.wg.Wait()
                return nil
            }
            return err
        }
        s.addConn(conn)
        s.wg.Add(1)
        go func() {
            defer s.wg.Done()
            defer s.removeConn(conn)
            s.handleConn(conn)
        }()
    }
}
```

Each accepted connection gets its own goroutine running `handleConn` in a loop until the client
disconnects or the parser errors out. This is the standard Go networking pattern, not a custom
scheduler: the runtime's netpoller parks a goroutine blocked in `conn.Read` off its OS thread, so
thousands of idle connections cost goroutine stack space, not thousands of blocked OS threads —
one goroutine per connection scales past what one-thread-per-connection would allow.

`handleConn` itself is a tight loop: parse one command (`resp.Read`), dispatch it, write one
reply (`resp.Write`), repeat; any error from either read or write ends the connection.

## Graceful shutdown

The subtle part: **closing the listener alone is not enough.** A connection sitting idle between
commands is blocked inside `resp.Read`, which has no context-aware variant — it just blocks on
`conn.Read` until bytes arrive or the socket is closed. Closing the listener stops new connections
from being *accepted*, but every already-open, currently-idle connection's goroutine would keep
waiting forever, and `Serve` would never return.

So shutdown does two things, both triggered by `ctx.Done()`:

```go
go func() {
    <-ctx.Done()
    ln.Close()          // stop accepting new connections
    s.closeAllConns()   // unblock every connection idle in resp.Read
}()
```

`ln.Close()` makes the blocked `Accept()` call in the main loop return `net.ErrClosed`, which is
treated as the normal shutdown signal (not an error) and triggers `s.wg.Wait()` before returning
`nil`. `closeAllConns()` iterates the `conns` map and closes every live connection; a `Close()` on
a connection currently blocked in `conn.Read` makes that read return an error immediately, which
unwinds `handleConn`'s loop, runs its `defer`s (`removeConn`, `s.wg.Done()`), and lets that
goroutine exit. Once every connection goroutine has exited, `wg.Wait()` in the accept loop
returns and `Serve` itself returns.

`addConn`/`removeConn`/`closeAllConns` are the three operations on the tracked-connections map,
each taking `connsMu` for the duration — connections come and go on separate goroutines from the
one running shutdown, so this has to be locked.

## Command dispatch (`dispatch.go`, `types.go`)

```go
type Command string

const (
    CmdSet  Command = "SET"
    CmdGet  Command = "GET"
    CmdDel  Command = "DEL"
    CmdScan Command = "SCAN"
    CmdPing Command = "PING"
)
```

`dispatch(database, cmd)` takes one parsed `resp.Value` (must be a non-empty `Array` of
`BulkString` elements — anything else is a protocol error) and maps `cmd.Elems[0]` (uppercased) to
one of the five `do*` handlers below, passing the remaining elements as arguments.

| Command | Args | Behavior | Reply |
|---|---|---|---|
| `SET key value` | 2 | `database.Put(key, value)` | `+OK` on success, else an `Error` |
| `GET key` | 1 | `database.Get(key)` | the value as a `BulkString`, or a **null** bulk string if absent |
| `DEL key [key ...]` | 1+ | `database.Delete(key)` per argument | `Integer` count of keys that existed *before* being deleted |
| `SCAN lower upper` | 2 | `database.Scan(lower, upper)`, drained fully | flat `Array` of alternating key, value bulk strings |
| `PING [message]` | 0 or 1 | no database access | `+PONG`, or the message echoed back as a `BulkString` if one was given |

Notes worth calling out explicitly:

- **`DEL`'s count is computed by checking existence *before* deleting**, one key at a time
  (`database.Get(a.Str)` then `database.Delete(a.Str)`) — there's no atomic "delete and tell me if
  it existed" primitive in `db.DB`, so this does two calls per key rather than one.
- **`SCAN` here is *not* real Redis `SCAN`.** Real Redis `SCAN` is a cursor-based, incremental
  iteration over the entire keyspace, designed so a single call never blocks the server for long
  and a client can resume where it left off. `doScan` is a direct wrapper over hcdb's own
  `database.Scan(lowerBound, upperBound)` range-scan iterator (see [db.md](db.md)) — it takes two
  explicit bound arguments, not a cursor, and returns every matching key/value pair from one call
  in a single flat array. This is a deliberate deviation from Redis semantics, not a partial
  implementation of the real thing: hcdb has a range-scan primitive already, and exposing it
  directly over RESP was simpler and more useful for this codebase's purposes than emulating
  cursor semantics on top of it. A client expecting real `SCAN` behavior (cursor argument,
  bounded batch size, `COUNT`/`MATCH` options) will not get it here.
- **`PING` never touches the database** — it's answered entirely inside `server`, so it works even
  under load or mid-flush, which is exactly why it's the right choice for the Docker healthcheck
  (see [deployment.md](deployment.md)).

Every handler returns a `resp.Value` reply; errors are always `resp.ErrorValue("ERR " + ...)`, a
convention this package applies but that `resp` itself has no opinion on (see [resp.md](resp.md)).

## Known limitations

- **No authentication, no `SELECT`/multiple databases, no transactions/`MULTI`.** One database,
  one namespace, no access control — anyone who can open a TCP connection can read and write
  everything.
- **`SCAN` is a full range scan per call, not cursor-based** (see above) — a very wide bound pair
  materializes the entire result as one in-memory `Array` before writing any of it back, unlike
  real Redis `SCAN`'s bounded-batch design.
- **`DEL`'s existence check is not atomic with the delete.** Between the `Get` and the `Delete`
  for one key, a concurrent writer could change what's there; the reported count reflects the
  state at the `Get`, not necessarily at the moment of deletion.
- **One goroutine per connection, no connection limit.** A large number of concurrent clients
  costs a goroutine and a buffered reader/writer each; there's no backpressure or maximum
  connection count.
- **Inherits every `db.DB` limitation this sits on top of** — notably that a `SCAN` can still hit
  a deleted SSTable file if compaction runs concurrently (see [db.md](db.md#known-limitations)).

## Related

- [resp.md](resp.md) — the wire format this package parses and writes
- [db.md](db.md) — what every command actually calls
- [deployment.md](deployment.md) — `cmd/hcdb-server`, Docker, and the healthcheck built on `PING`
