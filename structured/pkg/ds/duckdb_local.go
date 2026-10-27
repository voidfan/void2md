package ds

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DuckdbLocalStore loads files from a local directory.
type DuckdbLocalStore struct {
	duckdbBase

	// DirPath mirrors self.dir_path.
	DirPath string
}

// NewDuckdbLocalStore builds a local store; DBName is the directory stem. It
// connects at construction (like Python), so a failing `LOAD excel` surfaces
// here.
func NewDuckdbLocalStore(dirPath string) (DuckdbStore, error) {
	abs, err := filepath.Abs(dirPath)
	if err != nil {
		abs = dirPath
	}
	store := &DuckdbLocalStore{
		DirPath: abs,
		duckdbBase: duckdbBase{
			DBName: pathStem(abs),
		},
	}
	store.impl = store
	if _, err := store.getConn(); err != nil {
		return nil, err
	}
	return store, nil
}

// pathStem mirrors Python's Path(p).stem. Divergence from filepath.Ext: a
// dotfile like ".hidden" has no suffix, so its stem is ".hidden".
func pathStem(p string) string {
	base := filepath.Base(p)
	ext := filepath.Ext(base)
	if strings.HasPrefix(base, ".") && strings.Count(base, ".") == 1 {
		// ".hidden": Python suffix is empty, stem is ".hidden"
		return base
	}
	return strings.TrimSuffix(base, ext)
}

// pathExt mirrors Python's Path(p).suffix (leading dot included).
func pathExt(p string) string {
	base := filepath.Base(p)
	if strings.HasPrefix(base, ".") && strings.Count(base, ".") == 1 {
		return ""
	}
	return filepath.Ext(base)
}

// extNoDot mirrors Python's `file_path.suffix.split(".")[-1].lower()`; empty
// when there is no suffix.
func extNoDot(p string) string {
	suffix := pathExt(p)
	if suffix == "" {
		return ""
	}
	parts := strings.Split(suffix, ".")
	return strings.ToLower(parts[len(parts)-1])
}

// getConn opens an in-memory connection with unsigned extensions allowed and
// loads the excel extension.
func (s *DuckdbLocalStore) getConn() (duckdbConn, error) {
	conn, err := openDuckDBConn("?allow_unsigned_extensions=true")
	if err != nil {
		return nil, err
	}
	if err := duckdbLoadExtension(conn, "excel"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// connTest returns true when dir_path is a directory and load_data succeeds.
func (s *DuckdbLocalStore) connTest(ctx context.Context) (bool, error) {
	info, err := os.Stat(s.DirPath)
	if err != nil || !info.IsDir() {
		return false, nil
	}
	if err := s.loadData(ctx, nil); err != nil {
		return false, err
	}
	return true, nil
}

// getFiles lists one directory level, keeping regular files with supported
// extensions; name is the stem, path is absolute.
func (s *DuckdbLocalStore) getFiles(ctx context.Context) ([]*FileInfo, error) {
	entries, err := os.ReadDir(s.DirPath)
	if err != nil {
		return nil, err
	}
	files := []*FileInfo{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		full := filepath.Join(s.DirPath, entry.Name())
		info, err := os.Stat(full)
		if err != nil || !info.Mode().IsRegular() {
			// os.Stat follows symlinks like Python's is_file(); regular files only.
			continue
		}
		extension := extNoDot(entry.Name())
		if !supportedExtensions[extension] {
			continue
		}
		files = append(files, &FileInfo{
			Name:      pathStem(entry.Name()),
			Extension: extension,
			Path:      full,
		})
	}
	return files, nil
}

// insertData is a no-op for the local store (Python default `pass`).
func (s *DuckdbLocalStore) insertData(
	ctx context.Context, filename string, file io.Reader, meta map[string]any,
) (string, error) {
	return "", nil
}

// deleteData is a no-op for the local store (Python default `pass`).
func (s *DuckdbLocalStore) deleteData(ctx context.Context, filename string) error {
	return nil
}

// supportedExtensions is the union of the fastexcel and duckdb extension sets.
var supportedExtensions = func() map[string]bool {
	out := map[string]bool{}
	for k := range fastExcelSupportedExtensions {
		out[k] = true
	}
	for k := range duckdbSupportedExtensions {
		out[k] = true
	}
	return out
}()
