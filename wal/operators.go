package wal

const syncThreshold = 10

var putCounter int

func (walObj *WAL) Put(key string, value string) error {

	var keyByte = []byte(key)
	var valueByte = []byte(value)

	var entry Entry = Entry{Key: keyByte, Value: valueByte, Type: OP_PUT}

	if err := walObj.Append(entry); err != nil {
		return err
	}

	putCounter++
	if putCounter >= syncThreshold {
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

	var entry Entry = Entry{Key: keyByte, Value: valueByte, Type: OP_DELETE}

	if err := walObj.Append(entry); err != nil {
		return err
	}

	putCounter++
	if putCounter >= syncThreshold {
		if err := walObj.Sync(); err != nil {
			return err
		}
		putCounter = 0
	}

	return nil
}
