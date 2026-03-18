package wal

import (
	"bufio"
	"os"
)

const (
	OP_PUT           = uint8(0)
	OP_DELETE        = uint8(1)
	MAX_KEY_LENGTH   = 1024
	MAX_VALUE_LENGTH = 1048576
)

type WAL struct {
	File      *os.File
	BufWriter *bufio.Writer
}

type Entry struct {
	Type  uint8
	Key   []byte
	Value []byte
}
