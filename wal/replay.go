package wal

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"unsafe"
)

func (walObj *WAL) Replay() ([]Entry, error) {
	if err := walObj.BufWriter.Flush(); err != nil {
		return nil, err
	}

	if _, err := walObj.File.Seek(0, 0); err != nil {
		return nil, err
	}

	fileReader := bufio.NewReader(walObj.File)

	var entries []Entry
	var offset int64 = 0

	for {

		startOffset := offset

		// totalLength
		var totalLength uint32
		err := binary.Read(fileReader, binary.LittleEndian, &totalLength)
		if err == io.EOF {
			break
		}
		if err != nil {
			// corruption
			walObj.File.Truncate(startOffset)
			break
		}

		// Validate Total Length

		if totalLength > (MAX_KEY_LENGTH + MAX_VALUE_LENGTH + uint32(unsafe.Sizeof(totalLength))) {
			walObj.File.Truncate(startOffset)
			break
		}

		recordBuf := make([]byte, totalLength)

		if _, err := io.ReadFull(fileReader, recordBuf); err != nil {
			walObj.File.Truncate(startOffset)
			break
		}

		buffReader := bytes.NewReader(recordBuf)

		var keyLength uint32
		var valueLength uint32

		if err := binary.Read(buffReader, binary.LittleEndian, &keyLength); err != nil {
			walObj.File.Truncate(startOffset)
			break
		}

		if err := binary.Read(buffReader, binary.LittleEndian, &valueLength); err != nil {
			walObj.File.Truncate(startOffset)
			break
		}

		if keyLength > MAX_KEY_LENGTH || valueLength > MAX_VALUE_LENGTH {
			walObj.File.Truncate(startOffset)
			break
		}

		key := make([]byte, keyLength)
		value := make([]byte, valueLength)

		if _, err := io.ReadFull(buffReader, key); err != nil {
			walObj.File.Truncate(startOffset)
			break
		}

		if _, err := io.ReadFull(buffReader, value); err != nil {
			walObj.File.Truncate(startOffset)
			break
		}

		entries = append(entries, Entry{Key: key, Value: value})

		offset += int64(totalLength + uint32(unsafe.Sizeof(totalLength)))
	}

	return entries, nil
}
