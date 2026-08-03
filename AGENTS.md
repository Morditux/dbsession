# dbsession

Modular, secure session management library for Go web apps. Single package `dbsession` (module `github.com/Morditux/dbsession`, Go 1.24.1) at repo root; `example/` is a runnable HTTP demo. No build tags, no makefile, no external code deps beyond `modernc.org/sqlite` (CGO-free), `lib/pq`, `gomemcache`.

## Commands

- Test: `go test -count=1 ./...` (CI: `go test -v -count=1 ./...`)
- Vet: `go vet ./...`
- Run example: `go run ./example`

External-service tests self-skip when unavailable: Memcached on `127.0.0.1:11211`, Postgres via `POSTGRES_TEST_DSN` (defaults to `postgres://postgres:postgres@localhost:5432/dbsession_test?sslmode=disable`).

## Architecture

- `manager.go` — `Manager` + `Config`. Validates config (`ErrInvalidConfig`) before starting the cleanup goroutine; cookie handling; `Get`/`Save`/`Regenerate`/`Destroy`/`New`; `Close`/`CloseContext` (idempotent, store owned by manager unless `LeaveStoreOpen`); session-ID generation (`generateID`, crypto-seeded ChaCha8 rng from `sync.Pool`) and validation (`isValidID`, 32 lowercase hex).
- `session.go` — `Session` (thread-safe via `mu` RWMutex; persistence ops serialized by `opMu`), `SessionSnapshot`, `RestoreSession`, and the `Store` interface (`Get`/`Save`/`Delete`/`Cleanup`/`Close`, all context-aware).
- `pool.go` — `sync.Pool` for gob buffers/readers/ID buffers; `PutBuffer`/`PutReader` wipe data (`clear`) before reuse.
- `sqlite.go` / `postgres.go` / `memcached.go` — `Store` implementations. SQLite: WAL, `busy_timeout`, PRAGMAs injected into DSN, writes serialized by mutex, prepared statements.
- `*_test.go` at root — in-package tests (`package dbsession`) using `MockStore`; config/lifecycle/concurrency/security/size-limit suites.

## Conventions

- Sentinel errors via `errors.New` (`ErrSessionTooLarge`, `ErrInvalidSessionID`, `ErrInvalidConfig`); wrap all store errors with `fmt.Errorf("...: %w", err)`.
- Security is load-bearing: validate/regenerate IDs (anti-fixation), fail closed when old-session delete fails, never return expired sessions, wipe pooled buffers, `__Secure-`/`__Host-` cookie rules enforced in config validation. Don't weaken these.
- Perf-sensitive code avoids allocations: `sync.Pool`, skip gob encoding for empty sessions, precomputed hex lookup table. Keep that style for new hot paths.
- `Manager` owns its `Store` by default — `Close` closes it; tests must set `LeaveStoreOpen` when sharing a store.
- Tests: table-driven with `t.Run` subtests; external-store tests skip gracefully (`t.Skipf`) when the service is down. Benchmarks use `b.Skipf` for unavailable services.
- Docs: package doc in `doc.go` mirrors README (lifecycle, ownership, validation) — keep both in sync when behavior changes.

## Notes

- `optim.md` / `todo.md` contain design notes and a work log; not user docs.
