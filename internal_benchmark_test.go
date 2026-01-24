package dbsession

import "testing"

func BenchmarkIsValidID(b *testing.B) {
	// A valid 32-char hex ID
	id := "0123456789abcdef0123456789abcdef"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !isValidID(id) {
			b.Fatal("should be valid")
		}
	}
}

func BenchmarkGenerateID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := generateID()
		if err != nil {
			b.Fatal(err)
		}
	}
}
