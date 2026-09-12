package resp

import (
	"bufio"
	"fmt"
)

// Write serializes v onto w and flushes. Recurses for Array elements — a
// multi-element array is buffered as one write, not one syscall per element.
//
// Worked example — replies to a few commands on key "city" holding "pune":
//
//	GET city      BulkStringValue("pune")   -> "$4\r\npune\r\n"
//	GET nosuchkey NullBulkStringValue()     -> "$-1\r\n"
//	SET city pune SimpleStringValue("OK")   -> "+OK\r\n"
//	DEL city      IntegerValue(1)           -> ":1\r\n"
//	bad command   ErrorValue("ERR ...")     -> "-ERR ...\r\n"
//	SCAN a z      ArrayValue(k, v)          -> "*2\r\n$4\r\ncity\r\n$4\r\npune\r\n"
//
// GET and DEL both "return a number-ish thing" but use different types, and
// the reason is the rule for the whole protocol: GET returns whatever bytes
// the user stored (arbitrary length, possibly binary, possibly absent) so it
// must be a bulk string, while DEL returns a count the server computed, which
// is always a plain number — so it is an Integer. User bytes back out means
// bulk string; a server-computed fact means Integer.
//
// Replies may use all five types. Commands arriving the other way may only be
// arrays of bulk strings — see Read.
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
