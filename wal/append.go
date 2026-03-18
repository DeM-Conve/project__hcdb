package wal

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

// [totalLen][keyLen][valLen][key][value][crc32]

func (walObj *WAL) Append(entry Entry) error {

	dataBytes, err := writeDataBuff(entry)
	if err != nil {
		return err
	}

	checksum := crc32.ChecksumIEEE(dataBytes)

	totalLength := uint32(len(dataBytes)) + 4 // (Data + Checksum)

	if err := binary.Write(walObj.BufWriter, binary.LittleEndian, totalLength); err != nil {
		return err
	}

	if _, err := walObj.BufWriter.Write(dataBytes); err != nil {
		return err
	}

	if err := binary.Write(walObj.BufWriter, binary.LittleEndian, checksum); err != nil {
		return err
	}

	return nil
}

func writeDataBuff(entry Entry) ([]byte, error) {
	dataBuf := new(bytes.Buffer)

	keyLength := uint32(len(entry.Key))
	valueLength := uint32(len(entry.Value))

	if err := binary.Write(dataBuf, binary.LittleEndian, keyLength); err != nil {
		return nil, err
	}

	if err := binary.Write(dataBuf, binary.LittleEndian, valueLength); err != nil {
		return nil, err
	}

	if _, err := dataBuf.Write(entry.Key); err != nil {
		return nil, err
	}

	if _, err := dataBuf.Write(entry.Value); err != nil {
		return nil, err
	}

	return dataBuf.Bytes(), nil
}
