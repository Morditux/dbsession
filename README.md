# dbsession

![CI](https://github.com/Morditux/dbsession/actions/workflows/ci.yml/badge.svg)

A modular session management library for Go web applications using SQLite for persistence.

## Features

- CGO-free SQLite support via `modernc.org/sqlite`.
- Memcached support via `github.com/bradfitz/gomemcache/memcache`.
- Simple API for session management.
- Configurable TTL and automatic cleanup.
- Modular architecture.

## Installation

```bash
go get github.com/Morditux/dbsession
```

## Usage

### SQLite

```go
package main

import (
	"github.com/Morditux/dbsession"
	"time"
)

func main() {
	store, _ := dbsession.NewSQLiteStore("sessions.db")
	mgr := dbsession.NewManager(dbsession.Config{
		Store: store,
		TTL:   24 * time.Hour,
	})
	// Use mgr in your http handlers
}
```

### Memcached

```go
package main

import (
	"github.com/Morditux/dbsession"
	"time"
)

func main() {
	store := dbsession.NewMemcachedStore(24*time.Hour, "127.0.0.1:11211")
	mgr := dbsession.NewManager(dbsession.Config{
		Store: store,
		TTL:   24 * time.Hour,
	})
	// Use mgr in your http handlers
}
```

See [example/main.go](file:///home/mordicus/dev/go/dbsession/example/main.go) for a full example.
