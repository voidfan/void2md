package ds

import (
	"context"
	_ "embed"
	"strconv"
	"strings"

	"void2md/structured/pkg/metadata"
)

//go:embed metadata_sql/starrocks.sql
var starrocksMetadataSQL string

// StarRocks is the StarRocks data source (extends Mysql).
type StarRocks struct {
	Mysql
}

// NewStarRocks builds a StarRocks data source.
func NewStarRocks(host string, port int, username, password, dbName string, ssl bool) *StarRocks {
	s := &StarRocks{Mysql: *NewMysql(host, port, username, password, dbName, ssl)}
	s.metadataSQLText = starrocksMetadataSQL
	return s
}

func (s *StarRocks) Dialect() string { return "starrocks" }

// StringTypes is inherited from Mysql.

// JSONArrayAggFunc returns an empty string (the base value-example SQL is not
// used for StarRocks).
func (s *StarRocks) JSONArrayAggFunc() string { return "" }

// MetadataWithValueExamples is redeclared so the base implementation receives a
// *StarRocks (inheriting Mysql's would dispatch AggDistinctValuesSQL wrongly).
func (s *StarRocks) MetadataWithValueExamples(
	ctx context.Context, valueNum, valueStrMaxLength int,
) (*metadata.DatabaseMetadata, error) {
	return s.metadataWithValueExamples(ctx, s, s.DBName, 1, valueNum, valueStrMaxLength)
}

// AggDistinctValuesSQL builds the StarRocks value-example SQL. Python quirks
// preserved: \n before/after each statement, unconditional backticks, LIMIT
// uses the passed limit, and REPLACE's third argument is '\\"'.
func (s *StarRocks) AggDistinctValuesSQL(t *metadata.DBTable, limit int) (string, error) {
	cols := s.orderedColumns(t)
	colSQLs := make([]string, 0, len(cols))
	for _, name := range cols {
		colSQLs = append(colSQLs, starrocksColumnsSQL(name, t.Name, limit))
	}
	return strings.Join(colSQLs, "\nUNION ALL\n"), nil
}

func starrocksColumnsSQL(col, table string, limit int) string {
	return "\nSELECT\n" +
		"    '" + col + "' AS name,\n" +
		"    CONCAT('[', GROUP_CONCAT(CONCAT('\"' , REPLACE(" + col + ", '\"', '\\\\\"'),'\"')), ']') AS examples\n" +
		"FROM (\n" +
		"    SELECT DISTINCT `" + col + "`\n" +
		"    FROM `" + table + "`\n" +
		"    WHERE `" + col + "` IS NOT NULL\n" +
		"    LIMIT " + strconv.Itoa(limit) + "\n" +
		") AS t\n"
}
