package dbsession

import (
	"bytes"
	"testing"
)

// TestPutBufferVerifier verifies that PutBuffer zeroes out the used portion
// of the buffer before returning it to the pool.
func TestPutBufferVerifier(t *testing.T) {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()

	secret := []byte("My Secret Data")
	buf.Write(secret)

	// Get a view of the data before putting it back
	view := buf.Bytes()
	if !bytes.Equal(view, secret) {
		t.Fatalf("Sanity check failed: view does not contain secret")
	}

	// Create a copy of the view to check against later if needed,
	// but mostly we want to check that 'view' itself is zeroed.
	// Since 'view' points to the backing array, modifying the backing array
	// via PutBuffer should be reflected in 'view'.

	// Call the secure PutBuffer
	PutBuffer(buf)

	// Verify the data is wiped from the underlying array
	for i, b := range view {
		if b != 0 {
			t.Errorf("Byte at index %d was not zeroed! Got: %d", i, b)
		}
	}

	// Verify buf is reset (len 0) - although accessing buf after Put is technically race-prone
	// in a real concurrent env, here it is safe because we are single threaded test.
	if buf.Len() != 0 {
		t.Errorf("Buffer was not reset")
	}
}
