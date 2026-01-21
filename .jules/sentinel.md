## 2025-05-24 - Session Persistence & DoS Prevention
**Vulnerability:** Potential Denial of Service (DoS) via large session data and Race Conditions in SQLite.
**Learning:** `Manager.Save` enforces `MaxSessionBytes` by encoding to a temporary buffer (checked against limit) before passing to the store. It caches this encoded data in `Session.encoded` to avoid double-encoding in `SQLiteStore`.
**Prevention:** Always use the `Manager.Save` flow which handles size checks. `SQLiteStore` relies on the upper layer for size validation but handles concurrent write safety via `sync.Mutex` (serializing writes to prevent `SQLITE_BUSY` in WAL mode).

## 2025-05-24 - Cookie Security Defaulting
**Vulnerability:** Insecure cookies in modern browsers.
**Learning:** `Manager.NewManager` forcibly enables `Secure` attribute if `SameSite` is `None`, preventing browser rejection. It also defaults `HttpOnly` to true.
**Prevention:** Do not override these defaults without understanding browser security requirements.

## 2025-05-24 - Thread Safety in Manager.Save
**Vulnerability:** Race Condition causing Panic/DoS.
**Learning:** `Manager.Save` accessed `Session.Values` without locking the session mutex, causing data races if the application modified the session (via `Session.Set`) concurrently. This could lead to crashes or data corruption.
**Prevention:** `Manager.Save` must acquire `Session.mu.Lock()` (write lock) for the entire duration of the save operation, including the call to `Store.Save`, to ensure a consistent snapshot of the session data is persisted.

## 2025-05-24 - Fail-Safe Session Regeneration
**Vulnerability:** Session fixation risk due to "fail-open" regeneration.
**Learning:** `Manager.Regenerate` previously returned an error if deleting the old session failed, but left the user logged in with the *new* session ID (via cookie). This exposed the user to session fixation attacks (since the old session ID remained valid in the store).
**Prevention:** In `Regenerate`, if deletion of the old session fails, we must explicitly invalidate the new session (delete it and clear the cookie) to ensure the system fails closed (no session is better than an insecure session).

## 2025-05-24 - Data Remanence in Buffer Pools
**Vulnerability:** Sensitive session data persisting in memory across requests.
**Learning:** `bytes.Buffer.Reset()` does not clear the underlying array, only the internal pointers. When using `sync.Pool` to reuse buffers for sensitive data (like serialized sessions), old data remains in memory and could potentially leak or be exposed if the buffer capacity is accessed improperly or if memory is dumped.
**Prevention:** Buffers used for sensitive data must be explicitly zeroed out before being returned to the pool. We implemented a `PutBuffer` helper in `pool.go` that wipes the used portion of the buffer before resetting and pooling it.

## 2025-05-27 - Handling crypto/rand Failures in Go 1.24+
**Vulnerability:** Go 1.24+ `crypto/rand.Read` treats any error from the underlying reader as a fatal error that crashes the runtime, preventing graceful error handling (DoS risk if RNG fails transiently).
**Learning:** We cannot simply catch errors from `rand.Read`. To handle RNG errors gracefully (e.g., return 500 instead of crash), we must bypass the wrapper and use `io.ReadFull(rand.Reader, b)` directly.
**Prevention:** When robust error handling is required for random number generation, use `io.ReadFull(rand.Reader, ...)` and handle the returned error explicitly.

## 2025-05-27 - Defense-in-Depth Session Expiration
**Vulnerability:** Potential use of expired sessions if backend store has unreliable TTL or clock skew.
**Learning:** Relying solely on the storage backend (e.g., Memcached, Redis) to handle session expiration is risky. If the store's eviction is lazy, or if there is clock skew, the application might receive and accept an expired session.
**Prevention:** `Manager.Get` now explicitly checks `session.ExpiresAt.Before(time.Now())` immediately after retrieval. If expired, it treats the session as missing (returns a new one), ensuring the application never operates on expired data regardless of the backend's state.
