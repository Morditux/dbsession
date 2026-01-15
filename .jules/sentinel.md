## 2024-05-23 - Session ID Input Validation
**Vulnerability:** The `Manager.Get` method previously passed the raw session ID from the cookie directly to the backend store. While the SQLite store uses parameterized queries (preventing SQL injection), other stores or future implementations might be vulnerable to injection or DoS attacks if invalid inputs (e.g., extremely long strings or control characters) are processed.
**Learning:** Defense in depth is crucial. Even if the backend is secure, validating input at the boundary prevents unnecessary processing and potential exploit vectors.
**Prevention:** Added strict validation in `Manager.Get` to ensure the session ID matches the expected format (32 lowercase hex characters) before querying the store. Invalid IDs are treated as missing sessions (returning a new session).

## 2024-05-23 - Fail-Safe Session Destruction
**Vulnerability:** The `Manager.Destroy` method previously attempted to delete the session from the backend store before invalidating the client-side cookie. If the store operation failed (e.g., database downtime), the function would return an error early, leaving the valid session cookie on the user's browser. This created a "zombie session" state where the user believed they were logged out, but their credentials remained active on the client.
**Learning:** Security operations should "fail safe" (or "fail closed" for access, "fail open" for logout). When destroying a session, priority must be given to invalidating the client's token, as this is the user's primary interface with the session state. Backend inconsistencies can be reconciled later (TTL expiration), but a client retaining a token they intended to destroy is a security usability failure.
**Prevention:** Reordered operations in `Manager.Destroy` to unconditionally clear the cookie (set expiration to past) before attempting backend deletion. The error from the backend is still returned for logging/alerting, but the user is effectively logged out.

## 2024-05-23 - Max Session Size Limit (DoS Protection)
**Vulnerability:** The session manager previously had no limit on the size of data that could be stored in a session. This allowed a potential Denial of Service (DoS) attack where an attacker (or a bug) could stuff large amounts of data into a session, causing excessive memory usage during serialization or exhausting backend storage.
**Learning:** Always enforce upper bounds on user-controlled input sizes, even for internal structures like session data. Unbounded growth is a classic resource exhaustion vector.
**Prevention:** Introduced `MaxSessionBytes` configuration option (defaulting to unlimited for compatibility). The `Manager.Save` method now preemptively serializes session data to a temporary buffer to verify its size before attempting to save it to the store. If the limit is exceeded, it returns `ErrSessionTooLarge`.

## 2024-05-24 - Session ID Write-Side Validation
**Vulnerability:** While `Manager.Get` validated session IDs, `Manager.Save` did not. This allowed invalid session IDs to be persisted if manually set by the application, potentially leading to corrupt state or cookies that cannot be retrieved (since `Get` rejects them).
**Learning:** Validation must be symmetric. If you reject data on read, you should also reject it on write to prevent "write-only" data states and maintain system integrity.
**Prevention:** Added `isValidID` check to `Manager.Save` to enforce the same 32-hex-character constraint as `Manager.Get`.

## 2025-04-03 - Enforce Secure Attribute for SameSite=None
**Vulnerability:** The library allowed configuring `SameSite=None` without setting the `Secure` attribute. Modern browsers (like Chrome) reject cookies with `SameSite=None` unless they are also marked `Secure`. This could lead to a silent failure where sessions are not persisted, appearing as a functionality bug or availability issue.
**Learning:** When a security standard enforces a dependency between configuration options (SameSite=None => Secure=true), the library should enforce this dependency programmatically rather than relying on the user to configure it correctly. This prevents "foot-gun" configurations.
**Prevention:** Modified `NewManager` to automatically force the `Secure` flag to `true` whenever `SameSite` is set to `http.SameSiteNoneMode`.

## 2025-05-18 - Clear Session Data from Memory on Destroy
**Vulnerability:** Sensitive session data (PII, tokens) stored in the `Session.Values` map and internal buffers remained in memory even after `Manager.Destroy` was called. If the application retained a reference to the `Session` object, this data could persist until garbage collection, increasing the risk of memory scraping or accidental leakage.
**Learning:** Defense in depth includes data lifecycle management. Sensitive data should be purged from memory as soon as it is no longer needed, rather than relying solely on the garbage collector.
**Prevention:** Implemented `Session.Clear()` to nil out the `Values` map and internal buffers, and updated `Manager.Destroy` to invoke this method immediately after removing the session from the backend store.
