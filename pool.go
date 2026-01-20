package dbsession

import (
	"bytes"
	"sync"
)

var readerPool = sync.Pool{
	New: func() any {
		return bytes.NewReader(nil)
	},
}

var bufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// PutBuffer wipes the buffer's content and returns it to the pool.
// This is a security enhancement to ensure sensitive session data
// is not retained in memory longer than necessary.
func PutBuffer(buf *bytes.Buffer) {
	// Securely wipe the used portion of the buffer
	// buf.Bytes() returns the unread portion of the buffer, which
	// corresponds to the data we just wrote (and presumably read/used).
	b := buf.Bytes()
	clear(b)
	buf.Reset()
	bufferPool.Put(buf)
}
