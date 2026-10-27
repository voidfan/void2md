package ds

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"strings"

	"void2md/structured/pkg/metadata"
)

//go:embed metadata_sql/duckdb.sql
var duckdbMetadataSQL string

// DuckDB data-source family: shared base (duckdbBase) plus store types.
// Engine: duckdb-go/v2 pins DuckDB 1.5.5, deliberately matching the Python
// parity environment (not uv.lock's 1.4.x). Requires CGO; see
// duckdb_engine_nocgo.go for the CGO_ENABLED=0 stub.

// fastExcelSupportedExtensions mirrors Python's FASTEXCEL_SUPPORTED_EXTENSIONS.
var fastExcelSupportedExtensions = map[string]bool{
	"xlsx": true, "xls": true, "xlsm": true, "xlsb": true,
	"xla": true, "xlam": true, "ods": true,
}

// duckdbSupportedExtensions mirrors Python's DUCKDB_SUPPORTED_EXTENSIONS.
var duckdbSupportedExtensions = map[string]bool{
	"xlsx": true, "csv": true, "json": true, "parquet": true,
}

// goUnsupportedSpreadsheetExtensions are formats Python reads via
// fastexcel/xlrd but Go cannot (DuckDB's readers only handle xlsx/xlsm). They
// stay in fastExcelSupportedExtensions for parity but must be filtered out
// before registration.
var goUnsupportedSpreadsheetExtensions = map[string]bool{
	"xls": true, "xlsb": true, "ods": true, "xla": true, "xlam": true,
}

// errSpreadsheetUnsupportedInGo states the real reason a format cannot be read
// (no Go equivalent of fastexcel/xlrd).
func errSpreadsheetUnsupportedInGo(ext string) error {
	return fmt.Errorf(
		"the Go side has no reader implementation for %s (Python uses fastexcel/xlrd; DuckDB only supports xlsx/xlsm): the file will not be registered",
		ext,
	)
}

// readFileSQL builds the SELECT that makes DuckDB read a local file. Both load
// paths (by path / spilled in-memory bytes) must share it so the same file
// always gets the same column types and strictness. Divergence: Python's
// pyarrow/fastexcel paths have no strict_mode/all_varchar notion, so lenient
// DuckDB settings are used here.
func readFileSQL(path, ext string) string {
	switch ext {
	case "csv":
		return fmt.Sprintf("select * from read_csv('%s',strict_mode=false)", path)
	case "xlsx":
		return fmt.Sprintf("select * from read_xlsx('%s',all_varchar=true)", path)
	default:
		return fmt.Sprintf("select * from read_%s('%s')", ext, path)
	}
}

// FileInfo describes a file to load. Zero values mean "absent" (Python's None);
// empty Path/Data are falsey just like None in Python.
type FileInfo struct {
	Name      string
	Extension string
	Path      string
	Data      []byte
}

// duckdbConn is the minimal engine interface hiding the underlying driver.
type duckdbConn interface {
	Query(sql string) ([]string, [][]any, error)
	Exec(sql string) error
	Close() error
}

// duckdbOpener mirrors get_conn() of each subclass.
type duckdbOpener func() (duckdbConn, error)

// DuckdbStore is the external interface of the duckdb store family: DataSource
// plus the excel upload/delete methods.
type DuckdbStore interface {
	DataSource
	LoadData(ctx context.Context, files []*FileInfo) error
	GetNumRowsAndCols(ctx context.Context, tableName string) (int, int, error)
	// InsertData has a real implementation only for S3/Mongo; file is the
	// uploaded bytes (Python's BufferedReader/BinaryIO).
	InsertData(ctx context.Context, filename string, file io.Reader, meta map[string]any) (string, error)
	DeleteData(ctx context.Context, filename string) error
}

// duckdbImpl holds the per-subclass hooks, emulating Python's dynamic dispatch.
type duckdbImpl interface {
	getConn() (duckdbConn, error)
	getFiles(ctx context.Context) ([]*FileInfo, error)
	connTest(ctx context.Context) (bool, error)
	insertData(ctx context.Context, filename string, file io.Reader, meta map[string]any) (string, error)
	deleteData(ctx context.Context, filename string) error
}

// duckdbBase is the shared base of the duckdb stores.
type duckdbBase struct {
	baseDS

	impl duckdbImpl
	// DBName mirrors self.db_name.
	DBName string
	// conn is the lazily created engine connection.
	conn duckdbConn
	// loaded mirrors self.loaded.
	loaded bool
	// tmpDir holds temp files for in-memory loads; removed on Close.
	tmpDir string
}

func (b *duckdbBase) Dialect() string { return "duckdb" }

func (b *duckdbBase) StringTypes() map[string]bool {
	return map[string]bool{
		"VARCHAR": true, "CHAR": true, "BPCHAR": true, "TEXT": true, "STRING": true,
	}
}

func (b *duckdbBase) JSONArrayAggFunc() string { return "JSON_GROUP_ARRAY" }

// ensureConn returns the current connection, creating one on first use.
// Python connects in __init__; Go defers until first use (the store
// constructors still connect eagerly to keep the same error timing).
func (b *duckdbBase) ensureConn() (duckdbConn, error) {
	if b.conn != nil {
		return b.conn, nil
	}
	c, err := b.impl.getConn()
	if err != nil {
		return nil, err
	}
	b.conn = c
	return c, nil
}

// ConnTest dispatches to the subclass implementation.
func (b *duckdbBase) ConnTest(ctx context.Context) (bool, error) {
	return b.impl.connTest(ctx)
}

// ServerVersion parses "SELECT VERSION()" (e.g. "v1.5.5"; the base pattern
// consumes the leading v).
func (b *duckdbBase) ServerVersion(ctx context.Context) (*metadata.DBServerVersion, error) {
	_, rows, err := b.ExecuteRawSQL(ctx, "SELECT VERSION()")
	if err != nil {
		return nil, &ConnError{Msg: err.Msg}
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil, &ConnError{Msg: "Invalid version string format: "}
	}
	versionStr, _ := asString(rows[0][0])
	return parseServerVersion(versionStr, nil)
}

// LoadData registers all files as views. It is idempotent (returns early once
// loaded) and resets the connection first. nil files means "list files"; an
// explicit empty list does not trigger listing (Python's None vs []).
func (b *duckdbBase) LoadData(ctx context.Context, files []*FileInfo) error {
	return b.loadData(ctx, files)
}

func (b *duckdbBase) loadData(ctx context.Context, files []*FileInfo) error {
	if b.loaded {
		return nil
	}

	// Loading new data, so reset the connection.
	if b.conn != nil {
		_ = b.conn.Close()
	}
	conn, err := b.impl.getConn()
	if err != nil {
		return err
	}
	b.conn = conn

	if files == nil {
		files, err = b.impl.getFiles(ctx)
		if err != nil {
			return err
		}
	}

	for _, file := range files {
		if file == nil {
			continue
		}
		if file.Path != "" && duckdbSupportedExtensions[file.Extension] {
			// Python: self.conn.register(name, self.conn.sql(sql))
			if err := registerView(conn, file.Name, readFileSQL(file.Path, file.Extension)); err != nil {
				return err
			}
			continue
		}
		if fastExcelSupportedExtensions[file.Extension] {
			// Python logs and continues on unreadable files (never aborts). Go has
			// no fastexcel/xlrd, so unsupported spreadsheet formats report the real
			// reason and skip registration.
			if goUnsupportedSpreadsheetExtensions[file.Extension] {
				logFileError(file, errSpreadsheetUnsupportedInGo(file.Extension))
				continue
			}
			// Formats DuckDB can read but not in duckdbSupportedExtensions
			// (currently only xlsm). Scanned local files have Path but no Data, so
			// they must be read by path; only excel uploads carry Data.
			if file.Path != "" {
				if err := registerView(conn, file.Name, readFileSQL(file.Path, file.Extension)); err != nil {
					logFileError(file, err)
				}
				continue
			}
			if err := b.registerSpreadsheet(conn, file); err != nil {
				logFileError(file, err)
			}
			continue
		}
		if len(file.Data) > 0 && file.Extension == "csv" {
			// Python: df = read_csv(io.BytesIO(file.data)); self.conn.register(name, df)
			if err := b.registerCSVData(conn, file); err != nil {
				return err
			}
		}
	}

	b.loaded = true
	return nil
}

// registerCSVData registers in-memory csv bytes. Divergence: Python uses
// pyarrow; Go spills to a temp file and uses DuckDB's read_csv (see
// duckdb_excel.go header).
func (b *duckdbBase) registerCSVData(conn duckdbConn, file *FileInfo) error {
	return b.registerDataViaEngine(conn, file, "csv")
}

// registerSpreadsheet registers in-memory spreadsheet bytes. Divergence:
// Python's xls xlrd fallback has no Go equivalent (see
// goUnsupportedSpreadsheetExtensions).
func (b *duckdbBase) registerSpreadsheet(conn duckdbConn, file *FileInfo) error {
	return b.registerDataViaEngine(conn, file, file.Extension)
}

// ExecuteRawSQL lazily loads data, then runs the query. Only engine errors are
// turned into a SyntaxError (Python catches only duckdb.Error).
func (b *duckdbBase) ExecuteRawSQL(ctx context.Context, sqlText string) ([]string, [][]any, *metadata.SQLException) {
	if !b.loaded {
		if err := b.loadData(ctx, nil); err != nil {
			return emptyCols, emptyRows, &metadata.SQLException{
				ErrorType: metadata.SQLErrorSyntaxError,
				Msg:       err.Error(),
			}
		}
	}
	conn, err := b.ensureConn()
	if err != nil {
		return emptyCols, emptyRows, &metadata.SQLException{
			ErrorType: metadata.SQLErrorSyntaxError,
			Msg:       err.Error(),
		}
	}
	cols, rows, err := conn.Query(sqlText)
	if err != nil {
		return emptyCols, emptyRows, &metadata.SQLException{
			ErrorType: metadata.SQLErrorSyntaxError,
			Msg:       err.Error(),
		}
	}
	return cols, rows, nil
}

// Metadata loads data, then runs the metadata SQL.
func (b *duckdbBase) Metadata(ctx context.Context) (*metadata.DatabaseMetadata, error) {
	// Python: load_data exceptions bubble up as is (not wrapped in ConnError).
	if err := b.loadData(ctx, nil); err != nil {
		return nil, err
	}
	// The outer CAST(tables AS VARCHAR) is required: duckdb-go decodes JSON
	// columns into map/[]any (key order lost), while Python materializes them as
	// str. The CAST preserves key order, which DBTable.ColumnOrder depends on —
	// do not replace it with json.Marshal on the Go side.
	metadataSQL := renderTemplate(duckdbMetadataSQL, "{db_name}", b.DBName)
	metadataSQL = "SELECT db_name, CAST(tables AS VARCHAR) AS tables FROM (" + metadataSQL + ")"

	_, rows, sqlErr := b.ExecuteRawSQL(ctx, metadataSQL)
	if sqlErr != nil {
		return nil, &ConnError{Msg: sqlErr.Msg}
	}
	// tables JSON is in column 1 (column 0 is db_name).
	return b.buildMetadataFromRows(b.DBName, rows, 1)
}

// MetadataWithValueExamples is the shared base implementation.
func (b *duckdbBase) MetadataWithValueExamples(
	ctx context.Context, valueNum, valueStrMaxLength int,
) (*metadata.DatabaseMetadata, error) {
	return b.metadataWithValueExamples(ctx, b, b.DBName, 1, valueNum, valueStrMaxLength)
}

// AggDistinctValuesSQL is the shared base implementation.
func (b *duckdbBase) AggDistinctValuesSQL(t *metadata.DBTable, limit int) (string, error) {
	return b.aggDistinctValuesSQLBase(b, t, limit)
}

// GetNumRowsAndCols returns (row count, column count) of a table; (0, 0) when
// the result is missing or non-numeric.
func (b *duckdbBase) GetNumRowsAndCols(ctx context.Context, tableName string) (int, int, error) {
	if err := b.loadData(ctx, nil); err != nil {
		return 0, 0, err
	}
	conn, err := b.ensureConn()
	if err != nil {
		return 0, 0, err
	}
	sql := fmt.Sprintf(`
        SELECT
            (SELECT COUNT(*) FROM "%s") AS row_count,
            (SELECT COUNT(*)
             FROM information_schema.columns
             WHERE table_name = '%s') AS column_count
        `, tableName, tableName)
	_, rows, err := conn.Query(sql)
	if err != nil {
		return 0, 0, err
	}
	// Python returns (0, 0) when the row is not a tuple.
	if len(rows) == 0 || len(rows[0]) < 2 {
		return 0, 0, nil
	}
	r, ok1 := toInt(rows[0][0])
	c, ok2 := toInt(rows[0][1])
	if !ok1 || !ok2 {
		return 0, 0, nil
	}
	return r, c, nil
}

// InsertData dispatches to the subclass (the base default is a no-op).
func (b *duckdbBase) InsertData(
	ctx context.Context, filename string, file io.Reader, meta map[string]any,
) (string, error) {
	return b.impl.insertData(ctx, filename, file, meta)
}

// DeleteData dispatches to the subclass (the base default is a no-op).
func (b *duckdbBase) DeleteData(ctx context.Context, filename string) error {
	return b.impl.deleteData(ctx, filename)
}

// Close releases the engine connection and temp directory.
func (b *duckdbBase) Close() error {
	if b.conn == nil {
		return nil
	}
	err := b.conn.Close()
	b.conn = nil
	b.removeTmpDir()
	return err
}

// registerView implements Python's conn.register(name, relation) as
// CREATE OR REPLACE VIEW; identifiers are double-quote escaped.
func registerView(conn duckdbConn, name, query string) error {
	return conn.Exec("CREATE OR REPLACE VIEW " + quoteIdent(name) + " AS " + query)
}

// quoteIdent escapes an identifier with SQL double-quote rules.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// toInt converts numeric values like Python's int() (floats truncate; strings
// fail).
func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case int64:
		return int(x), true
	case int:
		return x, true
	case float64:
		return int(x), true
	case string:
		return 0, false
	default:
		return 0, false
	}
}
