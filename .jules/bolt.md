## 2024-05-23 - SQLite Prepared Statements
**Learning:** Using prepared statements (`sql.Stmt`) in Go with `modernc.org/sqlite` significantly reduces overhead for high-frequency operations like session retrieval and saving.
**Action:** Always prefer prepared statements for repeated queries in hot paths, but ensure proper cleanup in `Close()`.

## 2024-05-23 - Avoid Allocations with sql.RawBytes
**Learning:** For reading BLOBs that are immediately consumed (e.g. decoded), using `sql.RawBytes` avoids allocating a new `[]byte` slice and copying data.
**Action:** Use `QueryContext` (not `QueryRow`) and scan into `sql.RawBytes`, but ensure data is consumed before closing rows or next scan.
