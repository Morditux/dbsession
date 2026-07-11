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
	// Create session manager
	httpOnly := true
	secure := true
	mgr, err := dbsession.NewManager(dbsession.Config{
		Store:           store,
		TTL:             24 * time.Hour,
		CookieName:      "session_id",
		HttpOnly:        &httpOnly,
		Secure:          &secure,
		CleanupInterval: 10 * time.Minute,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer mgr.Close()

By default, Manager owns its Store: Manager.Close stops the cleanup worker and
then closes the Store. Applications that share one Store between managers must
set Config.LeaveStoreOpen on each manager and close the Store themselves after
all managers have stopped. CloseContext can be used to bound shutdown waiting.

NewManager validates configuration before starting background work and returns
an error for unsafe durations, invalid cookie scope, unsupported SameSite modes,
nil stores, and negative size limits. MustNewManager is available for programs
that treat invalid startup configuration as unrecoverable. Set DisableCleanup
instead of using a sentinel cleanup duration when expiration is managed elsewhere.

	// Use in HTTP handlers
	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		session, _ := mgr.Get(r)
		session.Set("authenticated", true)
		session.Set("user_id", 42)
		if err := mgr.Save(w, r, session); err != nil {
			http.Error(w, "Failed to save session", http.StatusInternalServerError)
		}
	})

Store Implementations:

  - SQLite: Uses modernc.org/sqlite for a CGO-free, embedded database experience.
  - PostgreSQL: uses github.com/lib/pq for robust, relational database storage.
  - Memcached: Uses github.com/bradfitz/gomemcache for high-performance, in-memory caching.

Thread Safety:

Manager, the built-in Store implementations, and Session methods are safe for
concurrent use. Session fields are private; use its accessors and mutation
methods. Persistence operations capture a point-in-time shallow snapshot and
are serialized per Session. Mutable values stored inside the values map remain
shared and must be copied or externally synchronized before concurrent mutation.
Session values must not be copied after first use. Custom Store implementations
can use Session.Snapshot and RestoreSession for persistence.
*/
package dbsession
