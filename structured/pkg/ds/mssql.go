package ds

import (
	"context"
	"database/sql"
	_ "embed"
	"strconv"
	"strings"

	_ "github.com/microsoft/go-mssqldb"

	"void2md/structured/pkg/metadata"
)

//go:embed metadata_sql/mssql.sql
var mssqlMetadataSQL string

// Mssql is the Microsoft SQL Server data source.
type Mssql struct {
	baseDS

	Host     string
	Port     int
	Username string
	Password string
	DBName   string
	Schema   string
	Encrypt  bool
	Timeout  int
}

// NewMssql builds a Mssql data source (schema defaults to "dbo").
func NewMssql(host string, port int, username, password, dbName, schema string, encrypt bool, timeout int) *Mssql {
	if schema == "" {
		schema = "dbo"
	}
	return &Mssql{
		Host: host, Port: port, Username: username, Password: password,
		DBName: dbName, Schema: schema, Encrypt: encrypt, Timeout: timeout,
	}
}

// Dialect returns "mssql". Known divergence: "mssql" is not a sqlglot dialect
// name (it is tsql), so SQLCheck falls back to startswith("SELECT") and
// SQLOptimization is a no-op — pre-existing Python behavior, preserved.
func (m *Mssql) Dialect() string { return "mssql" }

// StringTypes returns the string type names (lowercase for mssql).
func (m *Mssql) StringTypes() map[string]bool {
	return map[string]bool{
		"varchar": true, "nvarchar": true, "char": true,
		"nchar": true, "text": true, "ntext": true,
	}
}

func (m *Mssql) JSONArrayAggFunc() string { return "JSON_ARRAYAGG" }

// mssqlServerVersionSQL queries the server product version.
const mssqlServerVersionSQL = "SELECT CAST(SERVERPROPERTY('ProductVersion') AS VARCHAR) AS ProductVersion"

func (m *Mssql) connStr() string {
	s := "server=" + m.Host + ";" + "port=" + strconv.Itoa(m.Port) +
		";database=" + m.DBName + ";user id=" + m.Username + ";password=" + m.Password
	if m.Encrypt {
		s += ";encrypt=true"
	} else {
		s += ";encrypt=disable"
	}
	return s
}

func (m *Mssql) open(ctx context.Context) (*sql.DB, error) {
	db, err := sql.Open("sqlserver", m.connStr())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// mssqlMsg uses str(err) as the error message.
func mssqlMsg(err error) (string, int) { return err.Error(), 0 }

func (m *Mssql) ExecuteRawSQL(ctx context.Context, query string) ([]string, [][]any, *metadata.SQLException) {
	return execDB(ctx, query, m.open, mssqlMsg, mssqlMsg)
}

func (m *Mssql) ConnTest(ctx context.Context) (bool, error) {
	return m.connTestViaSelect1(ctx, m, "SELECT 1")
}

func (m *Mssql) ServerVersion(ctx context.Context) (*metadata.DBServerVersion, error) {
	_, rows, err := m.ExecuteRawSQL(ctx, mssqlServerVersionSQL)
	if err != nil {
		return nil, &ConnError{Msg: err.Msg}
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil, &ConnError{Msg: "Invalid version string format: "}
	}
	versionStr, _ := asString(rows[0][0])
	return parseServerVersion(versionStr, nil)
}

func (m *Mssql) Metadata(ctx context.Context) (*metadata.DatabaseMetadata, error) {
	sql := renderTemplate(mssqlMetadataSQL, "{{schema}}", m.Schema)
	_, rows, err := m.ExecuteRawSQL(ctx, sql)
	if err != nil {
		return nil, &ConnError{Msg: err.Msg}
	}
	return m.buildMetadataFromRows(m.DBName, rows, 1)
}

func (m *Mssql) MetadataWithValueExamples(
	ctx context.Context, valueNum, valueStrMaxLength int,
) (*metadata.DatabaseMetadata, error) {
	return m.metadataWithValueExamples(ctx, m, m.DBName, 1, valueNum, valueStrMaxLength)
}

// AggDistinctValuesSQL builds the mssql value-example SQL. Known divergences
// preserved from Python: "top 3" is hardcoded (the limit argument is ignored)
// and `'id' as name` uses a lowercase as.
func (m *Mssql) AggDistinctValuesSQL(t *metadata.DBTable, limit int) (string, error) {
	cols := m.orderedColumns(t)
	colSQLs := make([]string, 0, len(cols))
	for _, name := range cols {
		colSQLs = append(colSQLs, mssqlColumnsSQL(name, t.Name))
	}
	return strings.Join(colSQLs, "\nUNION ALL\n"), nil
}

func mssqlColumnsSQL(col, table string) string {
	return "\nSELECT\n" +
		"    '" + col + "' as name,\n" +
		"    CONCAT(\n" +
		"        '[',\n" +
		"        STRING_AGG(\n" +
		"            CONCAT('\"', REPLACE([" + col + "], '\"', '\\\"'), '\"'),\n" +
		"            ','\n" +
		"        ),\n" +
		"        ']'\n" +
		"    ) AS examples\n" +
		"FROM (\n" +
		"    SELECT DISTINCT top 3 [" + col + "]\n" +
		"    FROM [" + table + "]\n" +
		"    WHERE [" + col + "] IS NOT NULL\n" +
		") AS t\n"
}
