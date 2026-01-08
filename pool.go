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
