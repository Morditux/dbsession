package dbsession

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestNewManagerRejectsInvalidConfig(t *testing.T) {
	valid := Config{Store: &MockStore{}}
	var typedNilStore *MockStore

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"nil store", func(cfg *Config) { cfg.Store = nil }},
		{"typed nil store", func(cfg *Config) { cfg.Store = typedNilStore }},
		{"negative ttl", func(cfg *Config) { cfg.TTL = -time.Second }},
		{"sub-second ttl", func(cfg *Config) { cfg.TTL = time.Second - 1 }},
		{"negative cleanup interval", func(cfg *Config) { cfg.CleanupInterval = -time.Second }},
		{"negative cleanup when disabled", func(cfg *Config) {
			cfg.DisableCleanup = true
			cfg.CleanupInterval = -time.Second
		}},
		{"negative session limit", func(cfg *Config) { cfg.MaxSessionBytes = -1 }},
		{"negative same site", func(cfg *Config) { cfg.SameSite = http.SameSite(-1) }},
		{"unknown same site", func(cfg *Config) { cfg.SameSite = http.SameSiteNoneMode + 1 }},
		{"invalid cookie name", func(cfg *Config) { cfg.CookieName = "bad cookie" }},
		{"relative cookie path", func(cfg *Config) { cfg.CookiePath = "sessions" }},
		{"invalid cookie path", func(cfg *Config) { cfg.CookiePath = "/bad;path" }},
		{"invalid cookie domain", func(cfg *Config) { cfg.CookieDomain = "bad domain" }},
		{"insecure secure prefix", func(cfg *Config) { cfg.CookieName = "__Secure-session" }},
		{"insecure host prefix", func(cfg *Config) { cfg.CookieName = "__Host-session" }},
		{"host prefix with domain", func(cfg *Config) {
			secure := true
			cfg.Secure = &secure
			cfg.CookieName = "__Host-session"
			cfg.CookieDomain = "example.com"
		}},
		{"host prefix with scoped path", func(cfg *Config) {
			secure := true
			cfg.Secure = &secure
			cfg.CookieName = "__Host-session"
			cfg.CookiePath = "/app"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)

			mgr, err := NewManager(cfg)
			if mgr != nil {
				_ = mgr.Close()
				t.Fatal("NewManager returned a manager for invalid configuration")
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("NewManager error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestNewManagerAppliesDefaultsBeforeValidation(t *testing.T) {
	mgr, err := NewManager(Config{Store: &MockStore{}, DisableCleanup: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer mgr.Close()

	if mgr.ttl != 24*time.Hour {
		t.Fatalf("TTL = %v, want 24h default", mgr.ttl)
	}
	if mgr.cleanup != 10*time.Minute {
		t.Fatalf("cleanup interval = %v, want 10m default", mgr.cleanup)
	}
	if mgr.cookie != "session_id" || mgr.cookiePath != "/" {
		t.Fatalf("cookie defaults = %q %q, want session_id /", mgr.cookie, mgr.cookiePath)
	}
	if mgr.sameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", mgr.sameSite)
	}
}

func TestNewManagerDisableCleanupStartsNoWorker(t *testing.T) {
	store := &lifecycleStore{}
	mgr, err := NewManager(Config{Store: store, DisableCleanup: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	select {
	case <-mgr.workerDone:
	default:
		t.Fatal("workerDone is not closed when cleanup is disabled")
	}
	time.Sleep(5 * time.Millisecond)
	if got := store.cleanupCalls.Load(); got != 0 {
		t.Fatalf("Cleanup called %d times while disabled", got)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if got := store.closeCalls.Load(); got != 1 {
		t.Fatalf("store Close called %d times, want 1", got)
	}
}

func TestNewManagerAcceptsSecureCookiePrefixes(t *testing.T) {
	secure := true
	for _, name := range []string{"__Secure-session", "__Host-session"} {
		t.Run(name, func(t *testing.T) {
			mgr, err := NewManager(Config{
				Store:          &MockStore{},
				CookieName:     name,
				Secure:         &secure,
				DisableCleanup: true,
			})
			if err != nil {
				t.Fatalf("NewManager failed: %v", err)
			}
			defer mgr.Close()
		})
	}
}

func TestNewManagerValidatesCookieMaxAgeForPlatform(t *testing.T) {
	cfg := Config{
		Store:          &MockStore{},
		TTL:            time.Duration(int64(1)<<31) * time.Second,
		DisableCleanup: true,
	}
	mgr, err := NewManager(cfg)

	if strconv.IntSize == 32 {
		if mgr != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("32-bit NewManager = (%v, %v), want nil ErrInvalidConfig", mgr, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("64-bit NewManager failed: %v", err)
	}
	defer mgr.Close()
}

func TestMustNewManagerPanicsOnInvalidConfig(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("MustNewManager did not panic")
		}
	}()
	MustNewManager(Config{})
}
