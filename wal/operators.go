package wal

import "github.com/hchauhan7816/hcdb/config"

func (walObj *WAL) Put(keyByte []byte, valueByte []byte) error {
	return walObj.write(keyByte, valueByte)
}

func (walObj *WAL) Delete(keyByte []byte) error {
	return walObj.write(keyByte, []byte{})
}

// write appends one record and syncs every DEFAULT_SYNC_THRESHOLD writes.
// Put and Delete share it because the key's trailer already says which one
// this is — the record itself carries no separate type.
func (walObj *WAL) write(keyByte []byte, valueByte []byte) error {

	if err := walObj.Append(Entry{Key: keyByte, Value: valueByte}); err != nil {
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
