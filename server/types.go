package server

// Command is a supported command name, as it arrives in element 0 of a
// command array. Same pattern as resp.Type: a named type over the underlying
// wire representation, with one constant per legal value.
type Command string

const (
	CmdSet  Command = "SET"
	CmdGet  Command = "GET"
	CmdDel  Command = "DEL"
	CmdScan Command = "SCAN"
	CmdPing Command = "PING"
)
