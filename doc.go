/*
Package dbsession provides a modular and secure session management library for Go web applications.

It offers a unified API for managing user sessions with support for multiple persistence backends,
including SQLite (CGO-free), PostgreSQL, and Memcached. This allows for flexibility in deployment,
from simple single-server setups to distributed environments.

Key Features:

  - Modular Storage: Pluggable storage architecture supporting SQLite, PostgreSQL, and Memcached.
  - Security First:
  - Session ID regeneration to prevent session fixation attacks.
  - Strict session ID validation.
  - Secure default cookie settings (HttpOnly, SameSite).
  - Context-aware storage operations.
  - Performance:
  - Efficient session data serialization using gob.
  - Configurable maximum session size to prevent abuse.
  - Buffer pooling to reduce memory allocations.
  - Automatic Cleanup: Configurable background worker to remove expired sessions.

Usage:

To use dbsession, first initialize a storage backend (Store) and then create a Manager with your desired configuration.

	// Initialize SQLite store
	store, err := dbsession.NewSQLiteStore("sessions.db")
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	// Create session manager
	httpOnly := true
	secure := true
	mgr := dbsession.NewManager(dbsession.Config{
		Store:           store,
		TTL:             24 * time.Hour,
		CookieName:      "session_id",
		HttpOnly:        &httpOnly,
		Secure:          &secure,
		CleanupInterval: 10 * time.Minute,
	})
	defer mgr.Close()

	// Use in HTTP handlers
	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		session, _ := mgr.Get(r)
		session.Values["authenticated"] = true
		session.Values["user_id"] = 42
		if err := mgr.Save(w, r, session); err != nil {
			http.Error(w, "Failed to save session", http.StatusInternalServerError)
		}
	})

Store Implementations:

  - SQLite: Uses modernc.org/sqlite for a CGO-free, embedded database experience.
  - PostgreSQL: uses github.com/lib/pq for robust, relational database storage.
  - Memcached: Uses github.com/bradfitz/gomemcache for high-performance, in-memory caching.

Thread Safety:

The Manager and Store implementations are safe for concurrent use by multiple goroutines.
Individual Session objects are not thread-safe and should be handled within the scope of a single request.
*/
package dbsession
