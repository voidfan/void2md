package ds

import (
	"context"
	"database/sql"
	"time"

	"void2md/structured/pkg/metadata"
)

// openSQL opens a database/sql connection and pings it immediately, so a
// connection failure surfaces at open time (maps to Python's DBError branch).
func openSQL(ctx context.Context, driver, dsn string) (*sql.DB, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// queryRows runs SQL and returns (column names, rows). Values are normalized to
// what the Python drivers return ([]byte -> string, time.Time -> RFC3339).
func queryRows(ctx context.Context, db *sql.DB, query string) ([]string, [][]any, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	out := [][]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		for i, v := range vals {
			vals[i] = normalizeValue(v)
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return cols, out, nil
}

// normalizeValue converts a driver value to its Python-driver equivalent.
func normalizeValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case time.Time:
		// Python datetimes serialize as RFC3339 via orjson.
		return x.Format(time.RFC3339)
	default:
		return v
	}
}

// execDB is the shared ExecuteRawSQL skeleton for the SQL drivers: open failure
// maps to DBError (msg via openMsg), execution failure to SyntaxError (via
// runMsg).
func execDB(
	ctx context.Context, query string,
	open func(ctx context.Context) (*sql.DB, error),
	openMsg func(err error) (string, int),
	runMsg func(err error) (string, int),
) ([]string, [][]any, *metadata.SQLException) {
	if query == "" {
		return emptyCols, emptyRows, &metadata.SQLException{
			ErrorType: metadata.SQLErrorEmptySQL,
			Msg:       "SQL statement cannot be empty",
		}
	}

	db, err := open(ctx)
	if err != nil {
		msg, code := openMsg(err)
		return emptyCols, emptyRows, &metadata.SQLException{
			ErrorType: metadata.SQLErrorDBError,
			Msg:       msg,
			Code:      code,
		}
	}
	defer db.Close()

	cols, rows, err := queryRows(ctx, db, query)
	if err != nil {
		msg, code := runMsg(err)
		return emptyCols, emptyRows, &metadata.SQLException{
			ErrorType: metadata.SQLErrorSyntaxError,
			Msg:       msg,
			Code:      code,
		}
	}
	return cols, rows, nil
}

// emptyCols / emptyRows are the error-branch results. They must be non-nil
// empty slices: nil serializes to JSON null, but Python's [] serializes to [].
var (
	emptyCols = []string{}
	emptyRows = [][]any{}
)

// plainMsg uses str(err) as msg; code stays 0.
func plainMsg(err error) (string, int) { return err.Error(), 0 }

// argsMsg extracts the structured driver error when recognized, else falls back
// to err.Error(); code stays 0.
func argsMsg(err error) (string, int) { return err.Error(), 0 }
