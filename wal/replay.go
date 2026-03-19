package wal

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/hchauhan7816/hcdb/config"
)

type countingReader struct {
	r   io.Reader
	pos int64
}

func (c *countingReader) Read(p []byte) (n int, err error) {
	n, err = c.r.Read(p)
	c.pos += int64(n)
	return
}

func (walObj *WAL) Replay() ([]Entry, error) {
	if err := walObj.BufWriter.Flush(); err != nil {
		return nil, err
	}

	if _, err := walObj.File.Seek(0, 0); err != nil {
		return nil, err
	}

	// reader := bufio.NewReader(walObj.File)
	cr := &countingReader{r: walObj.File}
	var entries []Entry

	for {
		startOffset := cr.pos

		// totalLength
		var totalLength uint32
		err := binary.Read(cr, binary.LittleEndian, &totalLength)
		if err == io.EOF {
			break
		}
		if err != nil {
			// corruption
			fmt.Println("Error reading total length:", err)
			walObj.File.Truncate(startOffset)
			break
		}

		// Validate Total Length
		if totalLength > (1 + 4 + config.MAX_KEY_LENGTH + 4 + config.MAX_VALUE_LENGTH + 4) {
			fmt.Println("Total length is greater than expected:", totalLength)
			walObj.File.Truncate(startOffset)
			break
		}

		recordBuf := make([]byte, totalLength)

		if _, err := io.ReadFull(cr, recordBuf); err != nil {
			fmt.Println("Error reading record buffer:", err)
			walObj.File.Truncate(startOffset)
			break
		}

		// Split into data + crc
		if len(recordBuf) < 4 {
			fmt.Println("Record buffer is less than expected:", len(recordBuf))
			walObj.File.Truncate(startOffset)
			break
		}

		dataPart := recordBuf[:len(recordBuf)-4]
		storedCRC := binary.LittleEndian.Uint32(recordBuf[len(recordBuf)-4:])

		// Computed CRC
		computedCRC := crc32.ChecksumIEEE(dataPart)

		if computedCRC != storedCRC {
			// corruption
			fmt.Println("Computed CRC does not match stored CRC:", computedCRC, storedCRC)
			walObj.File.Truncate(startOffset)
			break
		}

		// Parse Data Part
		buffReader := bytes.NewReader(dataPart)

		var opType uint8

		if err := binary.Read(buffReader, binary.LittleEndian, &opType); err != nil {
			fmt.Println("Error reading type of event:", err)
			walObj.File.Truncate(startOffset)
			break
		}

		var keyLength uint32
		var valueLength uint32

		if err := binary.Read(buffReader, binary.LittleEndian, &keyLength); err != nil {
			fmt.Println("Error reading key length:", err)
			walObj.File.Truncate(startOffset)
			break
		}

		if err := binary.Read(buffReader, binary.LittleEndian, &valueLength); err != nil {
			fmt.Println("Error reading value length:", err)
			walObj.File.Truncate(startOffset)
			break
		}

		if keyLength > config.MAX_KEY_LENGTH || valueLength > config.MAX_VALUE_LENGTH {
			fmt.Println("Key length or value length is greater than expected:", keyLength, valueLength)
			walObj.File.Truncate(startOffset)
			break
		}

		key := make([]byte, keyLength)
		value := make([]byte, valueLength)

		if _, err := io.ReadFull(buffReader, key); err != nil {
			fmt.Println("Error reading key:", err)
			walObj.File.Truncate(startOffset)
			break
		}

		if _, err := io.ReadFull(buffReader, value); err != nil {
			fmt.Println("Error reading value:", err)
			walObj.File.Truncate(startOffset)
			break
		}

		entries = append(entries, Entry{Key: key, Value: value, Type: opType})
	}

	return entries, nil
}
