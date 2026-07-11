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
 // Create session manager with default security settings
 mgr, err := dbsession.NewManager(dbsession.Config{
  Store: store,
  TTL:   24 * time.Hour,
 })
 if err != nil {
  log.Fatal(err)
 }
 defer mgr.Close()

 // Use in HTTP handlers
 http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
  session, _ := mgr.Get(r)
  session.Set("user_id", 42)
  if err := mgr.Save(w, r, session); err != nil {
   http.Error(w, "Failed to save session", http.StatusInternalServerError)
  }
 })
}
```

## Lifecycle and store ownership

By default, a `Manager` owns its configured `Store`. Calling `Manager.Close`
stops the cleanup worker, waits for it to finish, and then closes the store.
`Close` is idempotent and safe for concurrent use, so do not also defer
`store.Close()` in this mode.

When several managers share one store, keep ownership in the caller:

```go
store, err := dbsession.NewSQLiteStore("sessions.db")
if err != nil {
 log.Fatal(err)
}
defer store.Close()

mgr, err := dbsession.NewManager(dbsession.Config{
 Store:          store,
 LeaveStoreOpen: true,
})
if err != nil {
 log.Fatal(err)
}
defer mgr.Close()
```

`CloseContext` can bound how long shutdown waits for an in-progress cleanup.
If its context expires, the store is deliberately left open; call `Close` or
`CloseContext` again after cleanup can finish.

## Configuration validation

`NewManager` validates its complete configuration before starting the cleanup
goroutine. It returns an error for nil stores, unsafe durations, invalid cookie
scope, unsupported `SameSite` values, and negative size limits. A zero `TTL`
selects the documented 24-hour default; explicit TTLs shorter than one second
are rejected. Use `DisableCleanup: true` to disable automatic cleanup rather
than a sentinel duration. `MustNewManager` is available when invalid startup
configuration should terminate the application.

Cookie domains broaden the session cookie to matching subdomains. Prefer an
empty `CookieDomain` for a host-only cookie. The library validates domain
syntax but does not determine whether a domain is an organizational or public
suffix boundary. `__Host-` cookies additionally require `Secure=true`, `/` as
their path, and an empty domain.

### Advanced Configuration

You can customize cookie settings and background cleanup intervals. Note that `HttpOnly` and `Secure` settings in `Config` take pointers to `bool`.

```go
    httpOnly := true
    secure := true

 mgr, err := dbsession.NewManager(dbsession.Config{
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
 if err != nil {
  log.Fatal(err)
 }
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

The `Manager`, built-in `Store` implementations, and `Session` methods are safe
for concurrent use. Session values and metadata are private and must be accessed
through `Get`, `Set`, `Delete`, `ValuesSnapshot`, `ID`, `CreatedAt`, and
`ExpiresAt`. `Save`, `Regenerate`, and `Destroy` persist point-in-time snapshots
and are serialized per session.

Snapshots are shallow: the values map is copied, but mutable maps, slices,
pointers, or structs stored inside it remain shared. Copy those values before
mutating them concurrently. A `Session` must not be copied after first use.

Custom stores can use `Session.Snapshot` before persistence and
`RestoreSession` when rebuilding a session returned by `Store.Get`.
