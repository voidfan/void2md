package ds

import (
	"context"
	"database/sql"
	_ "embed"
	"strconv"
	"strings"

	_ "github.com/sijms/go-ora/v2"

	"void2md/structured/pkg/metadata"
)

//go:embed metadata_sql/oracle.sql
var oracleMetadataSQL string

// Oracle is the Oracle data source.
type Oracle struct {
	baseDS

	Host     string
	Port     int
	Username string
	Password string
	DBName   string
	Schema   string
}

// NewOracle builds an Oracle data source; empty schema defaults to the
// uppercase username.
func NewOracle(host string, port int, username, password, dbName, schema string) *Oracle {
	if dbName == "" {
		dbName = "XEPDB1"
	}
	if schema == "" {
		schema = strings.ToUpper(username)
	}
	return &Oracle{
		Host: host, Port: port, Username: username, Password: password,
		DBName: dbName, Schema: schema,
	}
}

func (o *Oracle) Dialect() string { return "oracle" }

func (o *Oracle) StringTypes() map[string]bool {
	return map[string]bool{
		"VARCHAR": true, "NVARCHAR": true, "VARCHAR2": true, "NVARCHAR2": true,
		"CHAR": true, "NCHAR": true, "CLOB": true, "NCLOB": true,
	}
}

func (o *Oracle) JSONArrayAggFunc() string { return "JSON_ARRAYAGG" }

// ConnTest probes with "SELECT 1 from DUAL".
func (o *Oracle) ConnTest(ctx context.Context) (bool, error) {
	return o.connTestViaSelect1(ctx, o, "SELECT 1 from DUAL")
}

func (o *Oracle) dsn() string {
	return "oracle://" + o.Username + ":" + o.Password + "@" +
		o.Host + ":" + strconv.Itoa(o.Port) + "/" + o.DBName
}

func (o *Oracle) open(ctx context.Context) (*sql.DB, error) {
	return openSQL(ctx, "oracle", o.dsn())
}

// oracleMsg uses str(err) as the error message.
func oracleMsg(err error) (string, int) { return err.Error(), 0 }

func (o *Oracle) ExecuteRawSQL(ctx context.Context, query string) ([]string, [][]any, *metadata.SQLException) {
	return execDB(ctx, query, o.open, oracleMsg, oracleMsg)
}

// ServerVersion reports the server version. Divergence: Python reads
// conn.version from the handshake; go-ora does not expose it, so
// product_component_version is queried instead (same version string).
func (o *Oracle) ServerVersion(ctx context.Context) (*metadata.DBServerVersion, error) {
	db, err := o.open(ctx)
	if err != nil {
		return nil, &ConnError{Msg: err.Error()}
	}
	defer db.Close()

	versionStr, err := oracleServerVersionFromConn(ctx, db)
	if err != nil {
		return nil, &ConnError{Msg: err.Error()}
	}
	parts := strings.Split(versionStr, ".")
	if len(parts) < 2 {
		return nil, &ConnError{Msg: "Invalid version string format: " + versionStr}
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, &ConnError{Msg: "Invalid version string format: " + versionStr}
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, &ConnError{Msg: "Invalid version string format: " + versionStr}
	}
	v := &metadata.DBServerVersion{Major: major, Minor: minor}
	if len(parts) > 2 {
		if patch, err := strconv.Atoi(parts[2]); err == nil {
			v.Patch = &patch
		}
	}
	return v, nil
}

func oracleServerVersionFromConn(ctx context.Context, db *sql.DB) (string, error) {
	const q = "SELECT version FROM product_component_version WHERE UPPER(product) LIKE 'ORACLE DATABASE%'"
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", err
		}
		return s, nil
	}
	return "", rows.Err()
}

func (o *Oracle) Metadata(ctx context.Context) (*metadata.DatabaseMetadata, error) {
	sql := renderTemplate(oracleMetadataSQL, "{schema}", o.Schema)
	_, rows, err := o.ExecuteRawSQL(ctx, sql)
	if err != nil {
		return nil, &ConnError{Msg: err.Msg}
	}
	return o.buildMetadataFromRows(o.DBName, rows, 1)
}

func (o *Oracle) MetadataWithValueExamples(
	ctx context.Context, valueNum, valueStrMaxLength int,
) (*metadata.DatabaseMetadata, error) {
	return o.metadataWithValueExamples(ctx, o, o.DBName, 1, valueNum, valueStrMaxLength)
}

func (o *Oracle) AggDistinctValuesSQL(t *metadata.DBTable, limit int) (string, error) {
	return o.aggDistinctValuesSQLBase(o, t, limit)
}
