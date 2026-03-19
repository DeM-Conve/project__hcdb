package wal

import (
	"bufio"
	"os"
)

// Format: [totalLen][type(1)][keyLen(4)][valLen(4)][key][value][crc32(4)]

type WAL struct {
	File      *os.File
	BufWriter *bufio.Writer
}

type Entry struct {
	Type  uint8
	Key   []byte
	Value []byte
}
