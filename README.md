# dbsession

![CI](https://github.com/Morditux/dbsession/actions/workflows/ci.yml/badge.svg)

A modular, secure, and high-performance session management library for Go web applications.

## Features

- **Modular Storage**: Pluggable storage architecture supporting:
  - **SQLite**: CGO-free support via `modernc.org/sqlite`.
  - **PostgreSQL**: Robust support via `github.com/lib/pq`.
  - **Memcached**: High-performance caching via `github.com/bradfitz/gomemcache`.
- **Security First**:
  - Session ID regeneration to prevent session fixation attacks.
  - Strict session ID validation (32-char hex).
  - Secure default cookie settings (`HttpOnly`, `SameSite=Lax`).
  - Context-aware storage operations.
- **Performance**:
  - Efficient session data serialization using `gob`.
  - Configurable maximum session size.
  - Buffer pooling to reduce memory allocations.
- **Automatic Cleanup**: Built-in background worker to remove expired sessions.

## Installation

```bash
go get github.com/Morditux/dbsession
```

## Usage

### Basic Initialization (SQLite)

```go
package main

import (
 "log"
 "net/http"
 "time"

 "github.com/Morditux/dbsession"
)

func main() {
 // Initialize SQLite store
 store, err := dbsession.NewSQLiteStore("sessions.db")
 if err != nil {
  log.Fatal(err)
 }
 defer store.Close()

 // Create session manager with default security settings
 mgr := dbsession.NewManager(dbsession.Config{
  Store: store,
  TTL:   24 * time.Hour,
 })
 defer mgr.Close()

 // Use in HTTP handlers
 http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
  session, _ := mgr.Get(r)
  session.Values["user_id"] = 42
  if err := mgr.Save(w, r, session); err != nil {
   http.Error(w, "Failed to save session", http.StatusInternalServerError)
  }
 })
}
```

### Advanced Configuration

You can customize cookie settings and background cleanup intervals. Note that `HttpOnly` and `Secure` settings in `Config` take pointers to `bool`.

```go
    httpOnly := true
    secure := true

 mgr := dbsession.NewManager(dbsession.Config{
  Store:           store,
  TTL:             24 * time.Hour,
  CookieName:      "my_app_session",
  CookiePath:      "/",
  HttpOnly:        &httpOnly,
  Secure:          &secure, // Required if SameSite is None
  SameSite:        http.SameSiteStrictMode,
  CleanupInterval: 10 * time.Minute,
  MaxSessionBytes: 4096, // Limit session size to 4KB
 })
```

## Store Implementations

### PostgreSQL

```go
store, _ := dbsession.NewPostgreSQLStore("postgres://user:password@localhost/dbname?sslmode=disable")
```

### Memcached

```go
store := dbsession.NewMemcachedStore(24*time.Hour, "127.0.0.1:11211")
```

## Thread Safety

The `Manager` and `Store` implementations are safe for concurrent use. Individual `Session` objects are not thread-safe and should be handled within the scope of a single request.
