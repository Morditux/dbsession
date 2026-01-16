## 2025-05-24 - Session Persistence & DoS Prevention
**Vulnerability:** Potential Denial of Service (DoS) via large session data and Race Conditions in SQLite.
**Learning:** `Manager.Save` enforces `MaxSessionBytes` by encoding to a temporary buffer (checked against limit) before passing to the store. It caches this encoded data in `Session.encoded` to avoid double-encoding in `SQLiteStore`.
**Prevention:** Always use the `Manager.Save` flow which handles size checks. `SQLiteStore` relies on the upper layer for size validation but handles concurrent write safety via `sync.Mutex` (serializing writes to prevent `SQLITE_BUSY` in WAL mode).

## 2025-05-24 - Cookie Security Defaulting
**Vulnerability:** Insecure cookies in modern browsers.
**Learning:** `Manager.NewManager` forcibly enables `Secure` attribute if `SameSite` is `None`, preventing browser rejection. It also defaults `HttpOnly` to true.
**Prevention:** Do not override these defaults without understanding browser security requirements.
