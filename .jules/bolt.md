## 2024-05-23 - SQLite Prepared Statements
**Learning:** Using prepared statements (`sql.Stmt`) in Go with `modernc.org/sqlite` significantly reduces overhead for high-frequency operations like session retrieval and saving.
**Action:** Always prefer prepared statements for repeated queries in hot paths, but ensure proper cleanup in `Close()`.

## 2024-05-23 - Avoid Allocations with sql.RawBytes
**Learning:** For reading BLOBs that are immediately consumed (e.g. decoded), using `sql.RawBytes` avoids allocating a new `[]byte` slice and copying data.
**Action:** Use `QueryContext` (not `QueryRow`) and scan into `sql.RawBytes`, but ensure data is consumed before closing rows or next scan.

## 2024-05-24 - Avoid Double Encoding in Layered Architecture
**Learning:** When a manager layer encodes data for validation (e.g., size check) and a store layer encodes it for persistence, we pay the encoding cost twice.
**Action:** Cache the encoded result in the data object (temporarily) if the manager has already done the work, allowing the store to reuse it. Ensure proper invalidation if the object is mutable.
