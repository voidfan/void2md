package ds

import (
	"context"
	"database/sql"
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"void2md/structured/pkg/metadata"
)

//go:embed metadata_sql/sqlite.sql
var sqliteMetadataSQL string

// sqliteVersionSplit extracts major/minor/patch from the sqlite_version string.
var sqliteVersionSplit = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?`)

// Sqlite is the SQLite data source.
type Sqlite struct {
	baseDS

	DBPath string
	// DBName is the file stem (directory and extension stripped).
	DBName string
}

// NewSqlite builds a Sqlite data source; DBName is the path stem.
func NewSqlite(dbPath string) *Sqlite {
	base := filepath.Base(dbPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return &Sqlite{DBPath: dbPath, DBName: stem}
}

func (s *Sqlite) Dialect() string { return "sqlite" }

func (s *Sqlite) StringTypes() map[string]bool { return map[string]bool{"TEXT": true} }

func (s *Sqlite) JSONArrayAggFunc() string { return "JSON_GROUP_ARRAY" }

// ConnTest only checks that the file exists; it does not connect.
func (s *Sqlite) ConnTest(ctx context.Context) (bool, error) {
	info, err := os.Stat(s.DBPath)
	if err != nil {
		return false, nil
	}
	return !info.IsDir(), nil
}

// ServerVersion reports the SQLite library version via sqlite_version().
func (s *Sqlite) ServerVersion(ctx context.Context) (*metadata.DBServerVersion, error) {
	_, rows, err := s.ExecuteRawSQL(ctx, "SELECT sqlite_version()")
	if err != nil {
		return nil, &ConnError{Msg: err.Msg}
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil, &ConnError{Msg: "Invalid version string format: "}
	}
	versionStr, _ := asString(rows[0][0])
	m := sqliteVersionSplit.FindStringSubmatch(versionStr)
	if m == nil {
		return nil, &ConnError{Msg: "Invalid version string format: " + versionStr}
	}
	// Python: [int(i) for i in sqlite_version.split(".") if i.isdigit()]
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	v := &metadata.DBServerVersion{Major: major, Minor: minor}
	if m[3] != "" {
		patch, _ := strconv.Atoi(m[3])
		v.Patch = &patch
	}
	return v, nil
}

func (s *Sqlite) open(ctx context.Context) (*sql.DB, error) {
	return openSQL(ctx, "sqlite", s.DBPath)
}

// ExecuteRawSQL runs SQL; divergence: no empty-SQL short circuit, and both
// connection and execution errors are classified as SyntaxError.
func (s *Sqlite) ExecuteRawSQL(ctx context.Context, query string) ([]string, [][]any, *metadata.SQLException) {
	db, err := s.open(ctx)
	if err != nil {
		return emptyCols, emptyRows, &metadata.SQLException{
			ErrorType: metadata.SQLErrorSyntaxError,
			Msg:       err.Error(),
		}
	}
	defer db.Close()

	cols, rows, err := queryRows(ctx, db, query)
	if err != nil {
		return emptyCols, emptyRows, &metadata.SQLException{
			ErrorType: metadata.SQLErrorSyntaxError,
			Msg:       err.Error(),
		}
	}
	return cols, rows, nil
}

func (s *Sqlite) Metadata(ctx context.Context) (*metadata.DatabaseMetadata, error) {
	_, rows, err := s.ExecuteRawSQL(ctx, sqliteMetadataSQL)
	if err != nil {
		return nil, &ConnError{Msg: err.Msg}
	}
	// For SQLite the tables JSON is in column 0.
	return s.buildMetadataFromRows(s.DBName, rows, 0)
}

func (s *Sqlite) MetadataWithValueExamples(
	ctx context.Context, valueNum, valueStrMaxLength int,
) (*metadata.DatabaseMetadata, error) {
	return s.metadataWithValueExamples(ctx, s, s.DBName, 0, valueNum, valueStrMaxLength)
}

func (s *Sqlite) AggDistinctValuesSQL(t *metadata.DBTable, limit int) (string, error) {
	return s.aggDistinctValuesSQLBase(s, t, limit)
}
