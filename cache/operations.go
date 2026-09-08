package cache

func (c *LRU) Get(key string) ([]byte, bool) {
	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}

	c.ll.MoveToFront(elem)
	return elem.Value.(*entry).value, true
}

func (c *LRU) Put(key string, value []byte) {
	c.insert(key, value)
}

func (c *LRU) Len() int {
	return c.ll.Len()
}
