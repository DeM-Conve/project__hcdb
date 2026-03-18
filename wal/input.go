package wal

func (walObj *WAL) Put(key string, value string) error {

	var keyByte = []byte(key)
	var valueByte = []byte(value)

	var entry Entry = Entry{Key: keyByte, Value: valueByte}

	if err := walObj.Append(entry); err != nil {
		return err
	}

	return nil
}
