package resp

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
)

// Read parses one RESP value from r, recursing into Array elements.
//
// Worked example — a client sending SET port 8080 puts these bytes on the wire:
//
//	"*3\r\n$3\r\nSET\r\n$4\r\nport\r\n$4\r\n8080\r\n"
//
//	*3          array, 3 elements follow
//	$3  SET     bulk string, 3 bytes
//	$4  port    bulk string, 4 bytes
//	$4  8080    bulk string, 4 bytes
//
// Note $4 for 8080: the length counts CHARACTERS, not magnitude — the value is
// the text '8','0','8','0', not the number 8080. Commands carry no numeric type
// at all; every argument is a bulk string, because a key or value may be any
// bytes of any length. The ':' Integer type only ever appears in replies.
//
// Read produces:
//
//	Value{Type: Array, Elems: [
//	  {Type: BulkString, Str: "SET"},
//	  {Type: BulkString, Str: "port"},
//	  {Type: BulkString, Str: "8080"},
//	]}
//
// Built entirely on bufio.Reader's ReadString/ReadByte/Read, which block and
// retry against the underlying connection as needed — so a client that
// writes one byte at a time is indistinguishable from one that writes a
// whole command in one syscall. Nothing here assumes a full line has already
// arrived.
func Read(r *bufio.Reader) (Value, error) {
	line, err := readLine(r)
	if err != nil {
		return Value{}, err
	}
	if len(line) == 0 {
		return Value{}, fmt.Errorf("resp: empty line")
	}

	switch Type(line[0]) {
	case SimpleString:
		return Value{Type: SimpleString, Str: line[1:]}, nil

	case Error:
		return Value{Type: Error, Str: line[1:]}, nil

	case Integer:
		n, err := strconv.ParseInt(line[1:], 10, 64)
		if err != nil {
			return Value{}, fmt.Errorf("resp: bad integer %q: %w", line[1:], err)
		}
		return Value{Type: Integer, Num: n}, nil

	case BulkString:
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return Value{}, fmt.Errorf("resp: bad bulk length %q: %w", line[1:], err)
		}
		if n < 0 {
			return Value{Type: BulkString, Null: true}, nil
		}
		buf := make([]byte, n+2) // +2 for the trailing \r\n
		if _, err := io.ReadFull(r, buf); err != nil {
			return Value{}, err
		}
		return Value{Type: BulkString, Str: string(buf[:n])}, nil

	case Array:
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return Value{}, fmt.Errorf("resp: bad array length %q: %w", line[1:], err)
		}
		if n < 0 {
			return Value{Type: Array, Null: true}, nil
		}
		elems := make([]Value, n)
		for i := 0; i < n; i++ {
			elems[i], err = Read(r)
			if err != nil {
				return Value{}, err
			}
		}
		return Value{Type: Array, Elems: elems}, nil

	default:
		return Value{}, fmt.Errorf("resp: unknown type byte %q", line[0])
	}
}

// readLine reads up to \r\n and returns the line without it. bufio.Reader
// handles a \n split across separate underlying reads on its own.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	n := len(line)
	if n >= 2 && line[n-2] == '\r' {
		return line[:n-2], nil
	}
	return line[:n-1], nil
}
