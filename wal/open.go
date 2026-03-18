package wal

import (
	"bufio"
	"os"
)

func Open(filePath string) (*WAL, error) {

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)

	if err != nil {
		return nil, err
	}

	return &WAL{File: file, BufWriter: bufio.NewWriter(file)}, nil

}
