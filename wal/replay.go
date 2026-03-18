package wal

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

func (walObj *WAL) Replay() ([]Entry, error) {
	walObj.BufWriter.Flush()

	var entries []Entry
	fileReader := bufio.NewReader(walObj.File)

	for {
		var keyLength uint32
		var valueLength uint32

		if err := binary.Read(fileReader, binary.LittleEndian, &keyLength); err != nil {
			break
		}

		if err := binary.Read(fileReader, binary.LittleEndian, &valueLength); err != nil {
			return nil, fmt.Errorf("failed to read value length: %w", err)
		}

		if keyLength > MAX_KEY_LENGTH || valueLength > MAX_VALUE_LENGTH {
			return nil, fmt.Errorf("invalid key or value length: keyLength=%d, valueLength=%d", keyLength, valueLength)
		}

		key := make([]byte, keyLength)
		value := make([]byte, valueLength)

		if _, err := io.ReadFull(fileReader, key); err != nil {
			return nil, fmt.Errorf("failed to read key: %w", err)
		}

		if _, err := io.ReadFull(fileReader, value); err != nil {
			return nil, fmt.Errorf("failed to read value: %w", err)
		}

		entries = append(entries, Entry{Key: key, Value: value})
	}

	return entries, nil
}
