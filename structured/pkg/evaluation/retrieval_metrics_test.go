// Covers CalculateRetrievalMetrics / CalculateRetrievalMetricsFromSQL with the production
// sqlg.ParseRealCols. Expected values computed by the Python implementation on the same input.
package evaluation

import (
	"testing"

	"void2md/structured/pkg/metadata"
	"void2md/structured/pkg/sqlg"
)

// mkTable builds a table with columns in Python dict construction order.
func mkTable(name string, cols ...string) *metadata.DBTable {
	t := &metadata.DBTable{
		Name:        name,
		Columns:     map[string]*metadata.TableColumn{},
		ColumnOrder: cols,
		Enabled:     true,
	}
	for _, c := range cols {
		t.Columns[c] = metadata.NewTableColumn(c, "VARCHAR")
	}
	return t
}

func retrievalTables() []*metadata.DBTable {
	return []*metadata.DBTable{
		mkTable("users", "id", "name", "age"),
		mkTable("orders", "id", "user_id", "amt"),
	}
}

// TestCalculateRetrievalMetricsFromSQL: parity with Python calculate_retrieval_metrics,
// using the production sqlg.ParseRealCols as the injected parser.
func TestCalculateRetrievalMetricsFromSQL(t *testing.T) {
	tables := retrievalTables()

	tests := []struct {
		name            string
		sql             string
		dialect         string
		caseInsensitive bool
		want            [3]float64
	}{
		// Hits all 4 gold columns, but the recall set has 6 columns
		{"join_full", "SELECT u.name, o.amt FROM users u JOIN orders o ON u.id = o.user_id",
			"mysql", true, [3]float64{0.6666666666666666, 1.0, 0.8}},
		// Unqualified columns are completed to users.* after qualify
		{"bare", "SELECT id, amt FROM users", "mysql", true,
			[3]float64{0.16666666666666666, 0.5, 0.25}},
		{"partial", "SELECT users.name FROM users", "mysql", true,
			[3]float64{0.16666666666666666, 1.0, 0.2857142857142857}},
		{"nomatch", "SELECT x.zzz FROM someother x", "mysql", true,
			[3]float64{0.0, 0.0, 0.0}},
		// users.id inside the CTE is collected; the CTE name itself is excluded
		{"cte", "WITH c AS (SELECT id FROM users) SELECT id FROM c", "mysql", true,
			[3]float64{0.16666666666666666, 1.0, 0.2857142857142857}},
		// Parse failure → empty set → all three metrics 0 (and precision/recall are integer 0, not 0.0)
		{"empty", "SHOW TABLES", "mysql", true, [3]float64{0, 0, 0}},
		// case_insensitive=False: gold side is USERS.NAME, disjoint from the recall set (lowercase)
		{"ci_false", "SELECT USERS.NAME FROM USERS", "mysql", false,
			[3]float64{0.0, 0.0, 0.0}},
		{"postgres", "SELECT u.name FROM users u", "postgres", true,
			[3]float64{0.16666666666666666, 1.0, 0.2857142857142857}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, r, f1 := CalculateRetrievalMetricsFromSQL(
				tables, tt.sql, tt.dialect, tt.caseInsensitive, sqlg.ParseRealCols,
			)
			assertTriple(t, [3]float64{p, r, f1}, tt.want)
		})
	}
}

// TestCalculateRetrievalMetricsDirect: pure computation path with goldCols fed directly.
func TestCalculateRetrievalMetricsDirect(t *testing.T) {
	tables := retrievalTables()
	// Only users.name is in the recall set
	gold := map[string]struct{}{"users.name": {}, "users.zzz": {}}
	p, r, f1 := CalculateRetrievalMetrics(tables, gold, true)
	// tp=1, fp=5, fn=1 → precision=1/6, recall=1/2, f1=2*(1/6)*(1/2)/((1/6)+(1/2))=0.25
	assertTriple(t, [3]float64{p, r, f1}, [3]float64{1.0 / 6.0, 0.5, 0.25})
}
