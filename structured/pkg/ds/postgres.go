package ds

import (
	"context"
	"database/sql"
	_ "embed"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"void2md/structured/pkg/metadata"
)

//go:embed metadata_sql/postgres.sql
var postgresMetadataSQL string

// postgresVersionPattern is the Postgres-specific version regex.
var postgresVersionPattern = regexp.MustCompile(
	`^.*(?:PostgreSQL|EnterpriseDB) (\d+)\.?(\d+)?(?:\.(\d+))?(?:\.\d+)?(?:devel|beta)?`)

// Postgres is the PostgreSQL data source.
type Postgres struct {
	baseDS

	Host     string
	Port     int
	Username string
	Password string
	DBName   string
	Schema   string
	SSL      bool
}

// NewPostgres builds a Postgres data source.
func NewPostgres(host string, port int, username, password, dbName, schema string, ssl bool) *Postgres {
	return &Postgres{
		Host: host, Port: port, Username: username, Password: password,
		DBName: dbName, Schema: schema, SSL: ssl,
	}
}

func (p *Postgres) Dialect() string { return "postgres" }

func (p *Postgres) StringTypes() map[string]bool {
	return map[string]bool{
		"CHARACTER VARYING": true, "VARCHAR": true, "CHAR": true,
		"CHARACTER": true, "TEXT": true,
	}
}

func (p *Postgres) JSONArrayAggFunc() string { return "JSON_AGG" }

func (p *Postgres) dsn() string {
	sslmode := "disable"
	if p.SSL {
		sslmode = "require"
	}
	return "postgres://" + p.Username + ":" + p.Password + "@" +
		p.Host + ":" + strconv.Itoa(p.Port) + "/" + p.DBName + "?sslmode=" + sslmode
}

func (p *Postgres) open(ctx context.Context) (*sql.DB, error) {
	return openSQL(ctx, "pgx", p.dsn())
}

// pgMsg takes the server-side error message when available.
func pgMsg(err error) (string, int) {
	if pe, ok := err.(*pgconn.PgError); ok {
		return pe.Message, 0
	}
	return err.Error(), 0
}

func (p *Postgres) ExecuteRawSQL(ctx context.Context, query string) ([]string, [][]any, *metadata.SQLException) {
	return execDB(ctx, query, p.open, pgMsg, pgMsg)
}

func (p *Postgres) ConnTest(ctx context.Context) (bool, error) {
	return p.connTestViaSelect1(ctx, p, "SELECT 1")
}

func (p *Postgres) ServerVersion(ctx context.Context) (*metadata.DBServerVersion, error) {
	_, rows, err := p.ExecuteRawSQL(ctx, "SELECT VERSION()")
	if err != nil {
		return nil, &ConnError{Msg: err.Msg}
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil, &ConnError{Msg: "Invalid version string format: "}
	}
	versionStr, _ := asString(rows[0][0])
	return parseServerVersion(versionStr, postgresVersionPattern)
}

func (p *Postgres) Metadata(ctx context.Context) (*metadata.DatabaseMetadata, error) {
	sql := renderTemplate(postgresMetadataSQL, "{schema}", p.Schema)
	_, rows, err := p.ExecuteRawSQL(ctx, sql)
	if err != nil {
		return nil, &ConnError{Msg: err.Msg}
	}
	return p.buildMetadataFromRows(p.DBName, rows, 1)
}

func (p *Postgres) MetadataWithValueExamples(
	ctx context.Context, valueNum, valueStrMaxLength int,
) (*metadata.DatabaseMetadata, error) {
	return p.metadataWithValueExamples(ctx, p, p.DBName, 1, valueNum, valueStrMaxLength)
}

func (p *Postgres) AggDistinctValuesSQL(t *metadata.DBTable, limit int) (string, error) {
	return p.aggDistinctValuesSQLBase(p, t, limit)
}
