package wal

func (walObj *WAL) Sync() error {

	if err := walObj.BufWriter.Flush(); err != nil {
		return err
	}

	return nil

}
