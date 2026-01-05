## 2024-05-23 - SQLite Prepared Statements
**Learning:** Using prepared statements (`sql.Stmt`) in Go with `modernc.org/sqlite` significantly reduces overhead for high-frequency operations like session retrieval and saving.
**Action:** Always prefer prepared statements for repeated queries in hot paths, but ensure proper cleanup in `Close()`.
