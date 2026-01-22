## 2024-05-23 - SQLite Prepared Statements
**Learning:** Using prepared statements (`sql.Stmt`) in Go with `modernc.org/sqlite` significantly reduces overhead for high-frequency operations like session retrieval and saving.
**Action:** Always prefer prepared statements for repeated queries in hot paths, but ensure proper cleanup in `Close()`.

## 2024-05-23 - Avoid Allocations with sql.RawBytes
**Learning:** For reading BLOBs that are immediately consumed (e.g. decoded), using `sql.RawBytes` avoids allocating a new `[]byte` slice and copying data.
**Action:** Use `QueryContext` (not `QueryRow`) and scan into `sql.RawBytes`, but ensure data is consumed before closing rows or next scan.

## 2024-05-24 - Avoid Double Encoding in Layered Architecture
**Learning:** When a manager layer encodes data for validation (e.g., size check) and a store layer encodes it for persistence, we pay the encoding cost twice.
**Action:** Cache the encoded result in the data object (temporarily) if the manager has already done the work, allowing the store to reuse it. Ensure proper invalidation if the object is mutable.

## 2026-01-14 - SQLite Concurrent Reads
**Learning:** `modernc.org/sqlite` in WAL mode supports concurrent readers, but `database/sql` requires `MaxOpenConns > 1` to utilize them. However, multiple writers (via connection pool) can cause `SQLITE_BUSY` even with `busy_timeout`.
**Action:** Use a hybrid approach: Increase `MaxOpenConns` (e.g., 16) to allow parallel reads, but enforce a single-writer policy at the application level using a `sync.Mutex` around write operations (`Save`, `Delete`, `Cleanup`).

## 2026-01-20 - Skip Serialization for Empty Sessions
**Learning:** Even empty maps incur serialization overhead (allocating encoders, buffers). For size limits, empty structures can be safely skipped as they will virtually never exceed reasonable limits.
**Action:** Check `len(collection) > 0` before serializing for size validation to save allocations on the empty path.

## 2026-01-20 - Avoid Defer in Hot Paths
**Learning:** `defer` adds a small but measurable overhead (~50ns). In extremely hot paths like `Session.Get` or locking primitives called thousands of times per second, this accumulates.
**Action:** Explicitly call `Unlock()` or `RUnlock()` in critical sections/hot paths instead of using `defer`.

## 2026-01-20 - Use Built-in clear() for Buffers
**Learning:** Go 1.21+ introduced `clear()` for slices/maps. While benchmarks may show it as comparable to a loop, it is more idiomatic and allows the compiler to optimize (e.g. to `memclr` instructions) more effectively on supported architectures.
**Action:** Use `clear(b)` instead of `for i := range b { b[i] = 0 }`.

## 2026-02-01 - SQLite Connection Pool Configuration
**Learning:** `db.Exec("PRAGMA ...")` only affects the single connection used for that statement. When using `database/sql` connection pooling (default), subsequent queries may use other connections that lack these settings (e.g. `synchronous=FULL` instead of `NORMAL`), leading to performance degradation and `SQLITE_BUSY` errors.
**Action:** Use DSN query parameters (e.g., `?_pragma=synchronous=NORMAL&_pragma=busy_timeout=5000`) to ensure critical settings are applied to *every* connection created by the pool.

## 2026-02-05 - Safety over Micro-Optimizations
**Learning:** Removing `defer` in hot paths (`Manager.Save`) to save ~50ns (or 4%) was rejected because it compromises panic safety (potential deadlocks).
**Action:** Prioritize robustness. If `defer` overhead is a bottleneck, optimize the function logic first. Use manual cleanup only if strictly necessary and carefully reviewed (e.g., using `defer` for fallback).
