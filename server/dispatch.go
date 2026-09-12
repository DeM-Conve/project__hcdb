package server

import (
	"strings"

	"github.com/hchauhan7816/hcdb/db"
	"github.com/hchauhan7816/hcdb/resp"
)

func dispatch(database *db.DB, cmd resp.Value) resp.Value {

	if cmd.Type != resp.Array || len(cmd.Elems) == 0 {
		return resp.ErrorValue("ERR expected a non-empty command array")
	}

	for _, e := range cmd.Elems {
		if e.Type != resp.BulkString {
			return resp.ErrorValue("ERR command elements must be bulk strings")
		}
	}

	name := Command(strings.ToUpper(cmd.Elems[0].Str))
	args := cmd.Elems[1:]

	switch name {
	case CmdSet:
		return doSet(database, args)
	case CmdGet:
		return doGet(database, args)
	case CmdDel:
		return doDel(database, args)
	case CmdScan:
		return doScan(database, args)
	case CmdPing:
		return doPing(args)
	default:
		return resp.ErrorValue("ERR unknown command '" + cmd.Elems[0].Str + "'")
	}

}

func doSet(database *db.DB, args []resp.Value) resp.Value {
	if len(args) != 2 {
		return resp.ErrorValue("ERR wrong number of arguments for 'SET'")
	}
	if err := database.Put(args[0].Str, args[1].Str); err != nil {
		return resp.ErrorValue("ERR " + err.Error())
	}
	return resp.SimpleStringValue("OK")
}

func doGet(database *db.DB, args []resp.Value) resp.Value {
	if len(args) != 1 {
		return resp.ErrorValue("ERR wrong number of arguments for 'GET'")
	}
	val, ok := database.Get(args[0].Str)
	if !ok {
		return resp.NullBulkStringValue()
	}
	return resp.BulkStringValue(string(val))
}

func doDel(database *db.DB, args []resp.Value) resp.Value {
	if len(args) == 0 {
		return resp.ErrorValue("ERR wrong number of arguments for 'DEL'")
	}
	var removed int64
	for _, a := range args {
		if _, ok := database.Get(a.Str); ok {
			removed++
		}
		if err := database.Delete(a.Str); err != nil {
			return resp.ErrorValue("ERR " + err.Error())
		}
	}
	return resp.IntegerValue(removed)
}

// doScan is not real Redis SCAN (cursor-based key iteration over the whole
// keyspace) — it's a direct range scan over hcdb's own Feature 5 iterator:
// SCAN lowerBound upperBound, returning every key/value pair in range as one
// flat array. Same RESP framing either way, which is all this feature is
// actually testing.
func doScan(database *db.DB, args []resp.Value) resp.Value {
	if len(args) != 2 {
		return resp.ErrorValue("ERR wrong number of arguments for 'SCAN'")
	}
	it, err := database.Scan([]byte(args[0].Str), []byte(args[1].Str))
	if err != nil {
		return resp.ErrorValue("ERR " + err.Error())
	}

	var elems []resp.Value
	for it.Next() {
		elems = append(elems, resp.BulkStringValue(string(it.Key())))
		elems = append(elems, resp.BulkStringValue(string(it.Value())))
	}
	return resp.ArrayValue(elems...)
}

func doPing(args []resp.Value) resp.Value {
	if len(args) == 0 {
		return resp.SimpleStringValue("PONG")
	}
	return resp.BulkStringValue(args[0].Str)
}
