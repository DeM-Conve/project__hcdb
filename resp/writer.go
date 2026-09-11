package resp

import (
	"bufio"
	"fmt"
)

// Write serializes v onto w and flushes. Recurses for Array elements — a
// multi-element array is buffered as one write, not one syscall per element.
func Write(w *bufio.Writer, v Value) error {
	if err := write(w, v); err != nil {
		return err
	}
	return w.Flush()
}

func write(w *bufio.Writer, v Value) error {
	switch v.Type {
	case SimpleString:
		_, err := fmt.Fprintf(w, "%c%s\r\n", SimpleString, v.Str)
		return err
	case Error:
		_, err := fmt.Fprintf(w, "%c%s\r\n", Error, v.Str)
		return err
	case Integer:
		_, err := fmt.Fprintf(w, "%c%d\r\n", Integer, v.Num)
		return err
	case BulkString:
		if v.Null {
			_, err := fmt.Fprintf(w, "%c-1\r\n", BulkString)
			return err
		}
		_, err := fmt.Fprintf(w, "%c%d\r\n%s\r\n", BulkString, len(v.Str), v.Str)
		return err
	case Array:
		if v.Null {
			_, err := fmt.Fprintf(w, "%c-1\r\n", Array)
			return err
		}
		if _, err := fmt.Fprintf(w, "%c%d\r\n", Array, len(v.Elems)); err != nil {
			return err
		}
		for _, e := range v.Elems {
			if err := write(w, e); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("resp: unknown type %q", v.Type)
	}
}
