package ds

import (
	"context"
	_ "embed"
	"strconv"

	"database/sql"

	"github.com/go-sql-driver/mysql"

	"void2md/structured/pkg/metadata"
)

//go:embed metadata_sql/mysql.sql
var mysqlMetadataSQL string

// Mysql is the MySQL data source.
type Mysql struct {
	baseDS

	Host     string
	Port     int
	Username string
	Password string
	DBName   string
	SSL      bool

	// metadataSQLText is overridable so StarRocks can swap in starrocks.sql.
	metadataSQLText string
}

// NewMysql builds a Mysql data source.
func NewMysql(host string, port int, username, password, dbName string, ssl bool) *Mysql {
	return &Mysql{
		Host: host, Port: port, Username: username, Password: password,
		DBName: dbName, SSL: ssl, metadataSQLText: mysqlMetadataSQL,
	}
}

func (m *Mysql) Dialect() string { return "mysql" }

func (m *Mysql) StringTypes() map[string]bool {
	return map[string]bool{
		"CHAR": true, "VARCHAR": true, "TEXT": true,
		"TINYTEXT": true, "MEDIUMTEXT": true, "LONGTEXT": true,
	}
}

func (m *Mysql) JSONArrayAggFunc() string { return "JSON_ARRAYAGG" }

// dsn builds the go-sql-driver connection string (ssl_disabled=not ssl
// semantics).
func (m *Mysql) dsn() string {
	cfg := mysql.NewConfig()
	cfg.User = m.Username
	cfg.Passwd = m.Password
	cfg.Net = "tcp"
	cfg.Addr = m.Host + ":" + strconv.Itoa(m.Port)
	cfg.DBName = m.DBName
	if m.SSL {
		cfg.TLSConfig = "true"
	}
	return cfg.FormatDSN()
}

func (m *Mysql) open(ctx context.Context) (*sql.DB, error) {
	db, err := sql.Open("mysql", m.dsn())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// mysqlErr extracts (message, code) from a MySQLError.
func mysqlErr(err error) (string, int) {
	if me, ok := err.(*mysql.MySQLError); ok {
		return me.Message, int(me.Number)
	}
	return err.Error(), 0
}

// mysqlOpenErr handles connection failures: msg only, code stays 0.
func mysqlOpenErr(err error) (string, int) {
	if me, ok := err.(*mysql.MySQLError); ok {
		return me.Message, 0
	}
	return err.Error(), 0
}

func (m *Mysql) ExecuteRawSQL(ctx context.Context, query string) ([]string, [][]any, *metadata.SQLException) {
	return execDB(ctx, query, m.open, mysqlOpenErr, mysqlErr)
}

func (m *Mysql) ConnTest(ctx context.Context) (bool, error) {
	return m.connTestViaSelect1(ctx, m, "SELECT 1")
}

func (m *Mysql) ServerVersion(ctx context.Context) (*metadata.DBServerVersion, error) {
	_, rows, err := m.ExecuteRawSQL(ctx, "SELECT VERSION()")
	if err != nil {
		return nil, &ConnError{Msg: err.Msg}
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil, &ConnError{Msg: "Invalid version string format: "}
	}
	versionStr, _ := asString(rows[0][0])
	return parseServerVersion(versionStr, nil)
}

func (m *Mysql) metadataSQL() string {
	return renderTemplate(m.metadataSQLText, "{{db_name}}", m.DBName)
}

func (m *Mysql) Metadata(ctx context.Context) (*metadata.DatabaseMetadata, error) {
	_, rows, err := m.ExecuteRawSQL(ctx, m.metadataSQL())
	if err != nil {
		return nil, &ConnError{Msg: err.Msg}
	}
	return m.buildMetadataFromRows(m.DBName, rows, 1)
}

func (m *Mysql) MetadataWithValueExamples(ctx context.Context, valueNum, valueStrMaxLength int) (*metadata.DatabaseMetadata, error) {
	return m.metadataWithValueExamples(ctx, m, m.DBName, 1, valueNum, valueStrMaxLength)
}

func (m *Mysql) AggDistinctValuesSQL(t *metadata.DBTable, limit int) (string, error) {
	return m.aggDistinctValuesSQLBase(m, t, limit)
}

// ConnError is the Python-side `raise ConnectionError(err.msg)`; Msg is kept
// verbatim because upper layers consume it as a string.
type ConnError struct{ Msg string }

func (e *ConnError) Error() string { return e.Msg }
