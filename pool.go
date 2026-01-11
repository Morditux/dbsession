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
