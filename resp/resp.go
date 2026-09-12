package resp

type Type byte

const (
	SimpleString Type = '+'
	Error        Type = '-'
	Integer      Type = ':'
	BulkString   Type = '$'
	Array        Type = '*'
)

// Value is one parsed or to-be-written RESP value. Which fields are
// meaningful depends on Type:
//
//	SimpleString, Error  -> Str
//	Integer               -> Num
//	BulkString             -> Str, unless Null
//	Array                  -> Elems, unless Null
type Value struct {
	Type  Type
	Str   string
	Num   int64
	Elems []Value
	Null  bool // true for a null bulk string ($-1) or null array (*-1)
}

func SimpleStringValue(s string) Value { return Value{Type: SimpleString, Str: s} }
func ErrorValue(s string) Value        { return Value{Type: Error, Str: s} }
func IntegerValue(n int64) Value       { return Value{Type: Integer, Num: n} }
func BulkStringValue(s string) Value   { return Value{Type: BulkString, Str: s} }
func NullBulkStringValue() Value       { return Value{Type: BulkString, Null: true} }
func ArrayValue(elems ...Value) Value  { return Value{Type: Array, Elems: elems} }
func NullArrayValue() Value            { return Value{Type: Array, Null: true} }
