package wal

import (
	"encoding/binary"
	"os"
)

func (walObj *WAL) Replay() ([]Entry, error) {
	defer walObj.File.Close()

	var file *os.File = walObj.File
	var entries []Entry

	for {
		var keyLength uint32
		var valueLength uint32

		if err := binary.Read(file, binary.LittleEndian, &keyLength); err != nil {
			break
		}

		if err := binary.Read(file, binary.LittleEndian, &valueLength); err != nil {
			break
		}

		key := make([]byte, keyLength)
		value := make([]byte, valueLength)

		if _, err := file.Read(key); err != nil {
			break
		}

		if _, err := file.Read(value); err != nil {
			break
		}

		entries = append(entries, Entry{Key: key, Value: value})
	}

	return entries, nil
}
