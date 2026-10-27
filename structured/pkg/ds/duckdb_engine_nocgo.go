//go:build !cgo

package ds

import "errors"

// Stub duckdbConn implementation for CGO_ENABLED=0 builds: the duckdb driver
// needs CGO, so this keeps the package compiling while any actual use returns
// errDuckDBNeedsCGO. Python has no such branch (duckdb is a plain wheel).

// errDuckDBNeedsCGO is the deterministic error used when CGO_ENABLED=0.
var errDuckDBNeedsCGO = errors.New(
	"the duckdb data source requires CGO: rebuild with CGO_ENABLED=1 and a configured C compiler (UCRT mingw-w64 on Windows)")

func openDuckDBConn(dsn string) (duckdbConn, error) {
	return nil, errDuckDBNeedsCGO
}

// normalizeDuckDBValue degrades to the shared normalizeValue without CGO.
func normalizeDuckDBValue(v any) any { return normalizeValue(v) }

// duckdbLoadExtension fails immediately without CGO.
func duckdbLoadExtension(conn duckdbConn, name string) error { return errDuckDBNeedsCGO }
