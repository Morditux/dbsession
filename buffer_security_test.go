package dbsession

import (
	"bytes"
	"encoding/gob"
	"testing"
)

func TestPutReaderReleasesDecodedData(t *testing.T) {
	reader := bytes.NewReader([]byte("sensitive session data"))
	if reader.Len() == 0 || reader.Size() == 0 {
		t.Fatal("sanity check failed: reader does not reference test data")
	}

	PutReader(reader)

	if reader.Len() != 0 {
		t.Fatalf("reader still has %d readable bytes after PutReader", reader.Len())
	}
	if reader.Size() != 0 {
		t.Fatalf("reader still references %d bytes after PutReader", reader.Size())
	}
}

func BenchmarkGobDecodeReader(b *testing.B) {
	var encoded bytes.Buffer
	input := map[string]any{
		"user_id": 42,
		"roles":   []string{"user", "admin"},
		"token":   "representative-session-value",
	}
	if err := gob.NewEncoder(&encoded).Encode(input); err != nil {
		b.Fatal(err)
	}
	payload := encoded.Bytes()

	b.Run("pooled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			reader := readerPool.Get().(*bytes.Reader)
			reader.Reset(payload)
			var decoded map[string]any
			if err := gob.NewDecoder(reader).Decode(&decoded); err != nil {
				b.Fatal(err)
			}
			PutReader(reader)
		}
	})

	b.Run("new", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			reader := bytes.NewReader(payload)
			var decoded map[string]any
			if err := gob.NewDecoder(reader).Decode(&decoded); err != nil {
				b.Fatal(err)
			}
		}
	})
}

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
