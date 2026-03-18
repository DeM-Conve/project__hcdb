package wal

import (
	"bufio"
	"os"
)

type WAL struct {
	File      *os.File
	BufWriter *bufio.Writer
}

type Entry struct {
	Key   []byte
	Value []byte
}
