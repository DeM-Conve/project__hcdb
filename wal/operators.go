package wal

import "github.com/hchauhan7816/hcdb/config"

var putCounter int

func (walObj *WAL) Put(key string, value string) error {

	var keyByte = []byte(key)
	var valueByte = []byte(value)

	var entry Entry = Entry{Key: keyByte, Value: valueByte, Type: config.OP_PUT}

	if err := walObj.Append(entry); err != nil {
		return err
	}

	putCounter++
	if putCounter >= config.DEFAULT_SYNC_THRESHOLD {
		if err := walObj.Sync(); err != nil {
			return err
		}
		putCounter = 0
	}

	return nil
}

func (walObj *WAL) Delete(key string) error {

	var keyByte = []byte(key)
	var valueByte = []byte{}

	var entry Entry = Entry{Key: keyByte, Value: valueByte, Type: config.OP_DELETE}

	if err := walObj.Append(entry); err != nil {
		return err
	}

	putCounter++
	if putCounter >= config.DEFAULT_SYNC_THRESHOLD {
		if err := walObj.Sync(); err != nil {
			return err
		}
		putCounter = 0
	}

	return nil
}
