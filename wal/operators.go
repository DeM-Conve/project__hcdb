package wal

import "github.com/hchauhan7816/hcdb/config"

func (walObj *WAL) Put(keyByte []byte, valueByte []byte) error {

	var entry Entry = Entry{Key: keyByte, Value: valueByte, Type: config.OP_PUT}

	if err := walObj.Append(entry); err != nil {
		return err
	}

	walObj.putCounter++
	if walObj.putCounter >= config.DEFAULT_SYNC_THRESHOLD {
		if err := walObj.Sync(); err != nil {
			return err
		}
		walObj.putCounter = 0
	}

	return nil
}

func (walObj *WAL) Delete(keyByte []byte) error {

	var valueByte = []byte{}

	var entry Entry = Entry{Key: keyByte, Value: valueByte, Type: config.OP_DELETE}

	if err := walObj.Append(entry); err != nil {
		return err
	}

	walObj.putCounter++
	if walObj.putCounter >= config.DEFAULT_SYNC_THRESHOLD {
		if err := walObj.Sync(); err != nil {
			return err
		}
		walObj.putCounter = 0
	}

	return nil
}
