package resp

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
)

// Read parses one RESP value from r, recursing into Array elements.
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
