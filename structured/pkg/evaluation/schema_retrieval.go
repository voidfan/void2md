// Pure computation, mirroring Python structured/core/evaluation/: no IO, no side effects,
// outputs bit-identical to Python.
package evaluation

import (
	"strings"

	"void2md/structured/pkg/metadata"
	"void2md/structured/pkg/sqlutil"
)

// JoinChar corresponds to JOIN_CHAR in Python core/utils.py.
const JoinChar = "."

// Defaults matching calculate_retrieval_metrics' Python default arguments.
const (
	DefaultDialect         = "mysql"
	DefaultCaseInsensitive = true
)

// ParseRealColsFunc corresponds to parse_real_cols (Python core/utils.py). It depends on
// sqlglot qualify/traverse_scope (owned by sqlg/sqlutil), so it is injected; must be non-nil.
type ParseRealColsFunc func(sql string, dialect string, caseInsensitive bool) map[string]struct{}

// CalculateRetrievalMetricsFromSQL: full calculate_retrieval_metrics — parse gold columns
// from the gold SQL, then compute metrics.
func CalculateRetrievalMetricsFromSQL(
	tables []*metadata.DBTable,
	goldSQL string,
	dialect string,
	caseInsensitive bool,
	parseRealCols ParseRealColsFunc,
) (float64, float64, float64) {
	return CalculateRetrievalMetrics(tables, parseRealCols(goldSQL, dialect, caseInsensitive), caseInsensitive)
}

// CalculateRetrievalMetrics: metric computation of Python's calculate_retrieval_metrics.
// goldCols is parse_real_cols' output, already case-normalized (no second normalization).
// Note: Python uses table.columns.keys() (dict keys), not TableColumn.name.
func CalculateRetrievalMetrics(
	tables []*metadata.DBTable,
	goldCols map[string]struct{},
	caseInsensitive bool,
) (float64, float64, float64) {
	recallCols := make(map[string]struct{})
	for _, table := range tables {
		for cn := range table.Columns {
			colName := table.Name + JoinChar + cn
			if caseInsensitive {
				colName = strings.ToLower(colName)
			}
			recallCols[colName] = struct{}{}
		}
	}

	// tp = len(gold_cols & recall_cols)
	tp := 0
	for col := range goldCols {
		if _, ok := recallCols[col]; ok {
			tp++
		}
	}
	// fp = len(recall_cols - gold_cols)
	fp := 0
	for col := range recallCols {
		if _, ok := goldCols[col]; !ok {
			fp++
		}
	}
	// fn = len(gold_cols - recall_cols)
	fn := 0
	for col := range goldCols {
		if _, ok := recallCols[col]; !ok {
			fn++
		}
	}

	// Python passes ints to calculate_metrics for true division; matches float64 division.
	return sqlutil.CalculateMetrics(float64(tp), float64(fp), float64(fn))
}
