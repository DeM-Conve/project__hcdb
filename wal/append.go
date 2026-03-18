package wal

import (
	"encoding/binary"
	"unsafe"
)

func (walObj *WAL) Append(entry Entry) error {

	keyLength := uint32(len(entry.Key))
	valueLength := uint32(len(entry.Value))

	totalLength := keyLength + valueLength + uint32(unsafe.Sizeof(keyLength)) + uint32(unsafe.Sizeof(valueLength))

	if err := binary.Write(walObj.BufWriter, binary.LittleEndian, totalLength); err != nil {
		return err
	}

	if err := binary.Write(walObj.BufWriter, binary.LittleEndian, uint32(len(entry.Key))); err != nil {
		return err
	}

	if err := binary.Write(walObj.BufWriter, binary.LittleEndian, uint32(len(entry.Value))); err != nil {
		return err
	}

	if _, err := walObj.BufWriter.Write(entry.Key); err != nil {
		return err
	}

	if _, err := walObj.BufWriter.Write(entry.Value); err != nil {
		return err
	}

	return nil
}
