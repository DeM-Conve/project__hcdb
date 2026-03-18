package wal

import (
	"encoding/binary"
)

func (walObj *WAL) Append(entry Entry) error {

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
