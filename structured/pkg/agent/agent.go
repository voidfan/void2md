// Package agent corresponds to Python structured/core/agent.py.
package agent

import (
	"context"

	"void2md/structured/pkg/chatmodel"
	"void2md/structured/pkg/ds"
	"void2md/structured/pkg/metadata"
	"void2md/structured/pkg/sqleval"
	"void2md/structured/pkg/sqlg"
	"void2md/structured/pkg/sqlgen"
	"void2md/structured/pkg/sqlutil"
)

// SchemaFormat is the value domain of Python Agent's schema_format parameter.
type SchemaFormat = string

const (
	SchemaFormatMarkdown SchemaFormat = "markdown"
	SchemaFormatMSchema  SchemaFormat = "m_schema"
)

// Agent corresponds to Python's Agent. errorMsg uses "nil means falsy": Python checks
// `if not error_msg`, so empty strings are normalized to nil.
type Agent struct {
	DS              ds.DataSource
	GenerateSQLLLM  chatmodel.ChatModel
	SchemaFormat    SchemaFormat
	DBServerVersion *metadata.DBServerVersion
	SQLCheck        *sqlg.SQLCheck
	SQLOpt          *sqlg.SQLOptimization
	ResultNumLimit  int
	MaxAttempts     int
	// EvaluateSQLLLM nil = Python's evaluate_sql_llm=None.
	EvaluateSQLLLM chatmodel.ChatModel

	// Logger is optional (Python's log.debug/log.error); nil means no output. Minimal
	// interface satisfied by *slog.Logger (common/logger).
	Logger Logger
}

// Logger is the minimal logging interface used by the agent layer.
type Logger interface {
	Debug(msg string, args ...any)
	Error(msg string, args ...any)
}

// NewAgent corresponds to Agent.__init__; empty schemaFormat defaults to "m_schema",
// maxAttempts <= 0 defaults to 2.
func NewAgent(
	datasource ds.DataSource,
	dbServerVersion *metadata.DBServerVersion,
	generateSQLLLM chatmodel.ChatModel,
	resultNumLimit int,
	schemaFormat SchemaFormat,
	maxAttempts int,
	evaluateSQLLLM chatmodel.ChatModel,
) *Agent {
	if schemaFormat == "" {
		schemaFormat = SchemaFormatMSchema
	}
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	return &Agent{
		DS:              datasource,
		GenerateSQLLLM:  generateSQLLLM,
		SchemaFormat:    schemaFormat,
		DBServerVersion: dbServerVersion,
		SQLCheck:        sqlg.NewSQLCheck(datasource.Dialect()),
		SQLOpt:          sqlg.NewSQLOptimization(datasource.Dialect(), dbServerVersion.Major),
		ResultNumLimit:  resultNumLimit,
		MaxAttempts:     maxAttempts,
		EvaluateSQLLLM:  evaluateSQLLLM,
	}
}

// RunResult corresponds to the 6-tuple returned by Python's arun.
type RunResult struct {
	Answer            string
	SQL               string
	Cols              []string
	Data              [][]any
	TotalInputTokens  int
	TotalOutputTokens int
}

// ARun corresponds to Agent.arun. As in Python, ValueErrors from sql_gen.arun / sqleval.arun
// propagate upward uncaught.
func (a *Agent) ARun(
	ctx context.Context, query string, m *metadata.DatabaseMetadata, evidence string,
) (RunResult, error) {
	var answer *string
	sql := ""
	cols := []string{}
	data := [][]any{}
	totalInputTokens := 0
	totalOutputTokens := 0
	attempts := 0

	schemaStr := "# Database server info: " + a.DS.Dialect() + " " + a.DBServerVersion.String() + "\n"
	if m == nil {
		var err error
		m, err = a.DS.Metadata(ctx)
		if err != nil {
			return RunResult{}, err
		}
	}
	if a.Logger != nil {
		a.Logger.Debug("metadata: " + dumpJSON(m))
	}
	if a.SchemaFormat == SchemaFormatMarkdown {
		schemaStr += m.ToMarkdown()
	} else {
		schemaStr += m.ToMSchema()
	}

	// Mirrors Python: schema_type = {t.name: t.column_type() for t in metadata.tables if t.enabled}.
	// Order matters: column order determines the SELECT list order after star expansion.
	schemaInput := &sqlg.SchemaInput{}
	for _, t := range m.Tables {
		if !t.Enabled {
			continue
		}
		st := &sqlg.SchemaTable{Parts: []string{t.Name}}
		for _, c := range t.ColumnTypeOrdered(true) {
			st.Cols = append(st.Cols, sqlg.SchemaCol{Name: c.Name, Type: c.Type})
		}
		schemaInput.Tables = append(schemaInput.Tables, st)
	}

	var currentSQL *string
	var errorMsg *string

	// result assembles Python's various return points.
	result := func() RunResult {
		return RunResult{
			Answer:            derefOr(answer, ""),
			SQL:               sql,
			Cols:              cols,
			Data:              data,
			TotalInputTokens:  totalInputTokens,
			TotalOutputTokens: totalOutputTokens,
		}
	}

	for attempts < a.MaxAttempts {
		gen, err := sqlgen.Run(ctx, query, a.GenerateSQLLLM, schemaStr, evidence, currentSQL, errorMsg)
		if err != nil {
			return RunResult{}, err
		}
		answer, sql, errorMsg = gen.Answer, gen.SQL, gen.Error
		totalInputTokens += gen.InputTokens
		totalOutputTokens += gen.OutputTokens

		if answer != nil {
			return result(), nil
		}

		// Python: `elif error_msg is None and sql:` — empty sql is falsy.
		if errorMsg == nil && sql != "" {
			// Mirrors Python's try block (is_query + optimization only); a failure sets
			// error_msg = "SQL syntax error: ..." and keeps the pre-optimization sql.
			earlyReturn := false
			func() {
				isQuery, err := a.SQLCheck.IsQuery(sql)
				if err != nil {
					msg := "SQL syntax error: " + err.Error()
					errorMsg = &msg
					return
				}
				if !isQuery {
					// Mirrors `return answer or "", sql, ...`
					earlyReturn = true
					return
				}
				limit := a.ResultNumLimit
				optimized, optErr := a.SQLOpt.Run(sql, schemaInput, &limit)
				if optErr != nil {
					if a.Logger != nil {
						a.Logger.Error(optErr.Error())
					}
					msg := "SQL syntax error: " + optErr.Error()
					errorMsg = &msg
					return
				}
				// Replace sql only on success; keep the pre-optimization original on failure.
				sql = optimized
			}()
			if earlyReturn {
				return result(), nil
			}

			if errorMsg == nil {
				c, d, sqlErr := a.DS.ExecuteRawSQL(ctx, sql)
				cols, data = c, d
				if sqlErr != nil {
					if sqlErr.ErrorType != metadata.SQLErrorSyntaxError {
						return result(), nil
					}
					// Python assigns error_msg = err.msg unconditionally; empty msg is still
					// falsy, so skipping the assignment on "" is equivalent.
					if sqlErr.Msg != "" {
						msg := sqlErr.Msg
						errorMsg = &msg
					}
				}
			}

			if errorMsg == nil && a.EvaluateSQLLLM != nil {
				ev, err := sqleval.Run(ctx, query, a.EvaluateSQLLLM, schemaStr, sql, data, &evidence)
				if err != nil {
					return RunResult{}, err
				}
				totalInputTokens += ev.InputTokens
				totalOutputTokens += ev.OutputTokens
				ans := ev.Answer
				answer = &ans
				errorMsg = ev.Error
			}
		}

		if errorMsg == nil {
			break
		}
		attempts++
		current := sql
		currentSQL = &current
	}

	return result(), nil
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

// dumpJSON is for debug logging (Python's metadata.model_dump_json()). Known divergence:
// Go sorts map keys while Python keeps dict order, so column key order may differ —
// log-only, no effect on return values.
func dumpJSON(m *metadata.DatabaseMetadata) string {
	b, err := sqlutil.MarshalJSON(m)
	if err != nil {
		return ""
	}
	return string(b)
}
