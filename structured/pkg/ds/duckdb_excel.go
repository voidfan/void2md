package ds

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Registration branch for files held in memory (file.Data) rather than at a
// path. Go has no fastexcel/pyarrow equivalent: bytes are spilled to a temp
// file and read by DuckDB's own readers. Known differences vs Python: csv uses
// DuckDB type inference instead of pyarrow's; xlsx/xlsm are read with
// all_varchar=true (all columns become strings); xls/xlsb/ods/xla/xlam cannot
// be read at all (logged and skipped on scan, rejected at upload).

// duckdbRegisterableExtensions lists extensions DuckDB's built-in readers can
// read directly (no read_xls/read_xlsb/read_ods exist).
var duckdbRegisterableExtensions = map[string]bool{
	"csv":     true,
	"xlsx":    true,
	"xlsm":    true,
	"json":    true,
	"parquet": true,
}

// IngestableExtension reports whether the engine can read this extension. This
// is a capability query; caller allowlists (e.g. the upload allowlist in the
// api layer) are policy and deliberately narrower — do not merge the two, but
// every allowlisted format must return true here.
func IngestableExtension(ext string) bool {
	return duckdbRegisterableExtensions[ext] && !goUnsupportedSpreadsheetExtensions[ext]
}

// registerDataViaEngine spills in-memory file bytes to a temp file, then lets the
// engine read it and register it as a view.
func (b *duckdbBase) registerDataViaEngine(conn duckdbConn, file *FileInfo, ext string) error {
	if !duckdbRegisterableExtensions[ext] {
		return fmt.Errorf("unsupported extension: %s", ext)
	}
	dir, err := b.ensureTmpDir()
	if err != nil {
		return err
	}
	base := sanitizeFileName(file.Name)
	if base == "" {
		base = "data"
	}
	path := filepath.Join(dir, base+"."+ext)
	if err := os.WriteFile(path, file.Data, 0o644); err != nil {
		return err
	}
	// Read parameters come from readFileSQL, shared with the load-by-path branch
	// so both entry points treat the same file identically.
	return registerView(conn, file.Name, readFileSQL(path, ext))
}

// ensureTmpDir lazily creates the temp directory. Temp files must live until
// Close (views are evaluated lazily), so removal happens only there.
func (b *duckdbBase) ensureTmpDir() (string, error) {
	if b.tmpDir != "" {
		return b.tmpDir, nil
	}
	dir, err := os.MkdirTemp("", "structured-duckdb-")
	if err != nil {
		return "", err
	}
	b.tmpDir = dir
	return dir, nil
}

// removeTmpDir deletes the temp directory (called from Close).
func (b *duckdbBase) removeTmpDir() {
	if b.tmpDir == "" {
		return
	}
	_ = os.RemoveAll(b.tmpDir)
	b.tmpDir = ""
}

// sanitizeFileName strips characters illegal in a single-level file name.
func sanitizeFileName(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "\x00", "")
	return strings.TrimSpace(replacer.Replace(name))
}

// logFileError logs an unreadable file to stderr and lets load_data continue
// (Python logs via its default stderr StreamHandler and does not abort).
func logFileError(file *FileInfo, err error) {
	fmt.Fprintf(os.Stderr, "unsupported file: %s, extension: %s, read error: %v\n",
		file.Name, file.Extension, err)
}
