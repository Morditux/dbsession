package dbsession

import (
	"testing"
	"time"
)

func TestCalculateMemcachedExpiration(t *testing.T) {
	now := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		expiresAt time.Time
		ttl       time.Duration
		want      int32
	}{
		{
			name:      "Short TTL (1 hour)",
			expiresAt: time.Time{}, // Zero
			ttl:       time.Hour,
			want:      3600, // Delta
		},
		{
			name:      "Short Expiration (1 hour from now)",
			expiresAt: now.Add(time.Hour),
			ttl:       24 * time.Hour, // Should be ignored
			want:      3600, // Delta
		},
		{
			name:      "Long TTL (60 days) - Use Timestamp",
			expiresAt: time.Time{},
			ttl:       60 * 24 * time.Hour,
			want:      int32(now.Add(60 * 24 * time.Hour).Unix()), // Timestamp
		},
		{
			name:      "Long Expiration (60 days from now) - Use Timestamp",
			expiresAt: now.Add(60 * 24 * time.Hour),
			ttl:       time.Hour, // Should be ignored
			want:      int32(now.Add(60 * 24 * time.Hour).Unix()), // Timestamp
		},
		{
			name:      "Exact 30 Days (Delta)",
			expiresAt: time.Time{},
			ttl:       30 * 24 * time.Hour,
			want:      int32(30 * 24 * 3600), // Delta
		},
		{
			name:      "30 Days + 1 Second (Timestamp)",
			expiresAt: time.Time{},
			ttl:       30*24*time.Hour + time.Second,
			want:      int32(now.Add(30*24*time.Hour + time.Second).Unix()), // Timestamp
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateMemcachedExpiration(now, tt.expiresAt, tt.ttl)
			if got != tt.want {
				t.Errorf("calculateMemcachedExpiration() = %v, want %v", got, tt.want)
			}
		})
	}
}
