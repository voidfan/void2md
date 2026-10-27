package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"void2md/structured/pkg/agent"
	"void2md/structured/pkg/chatmodel"
	"void2md/structured/pkg/ds"
	"void2md/structured/pkg/sqlg"
	"void2md/structured/pkg/sqlutil"
)

// maxExcelFileSize mirrors Python's MAX_EXCEL_FILE_SIZE (env var, unit MB, default 50).
// A non-integer env var panics at init, matching Python's import-time ValueError.
var maxExcelFileSize = func() int {
	raw := os.Getenv("MAX_EXCEL_FILE_SIZE")
	if raw == "" {
		return 50 * 1024 * 1024
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		panic("MAX_EXCEL_FILE_SIZE is not a valid integer: " + raw)
	}
	return n * 1024 * 1024
}()

// excelSupportedFileExtensions is the upload whitelist. Divergence: Python also allows "xls",
// but Go has no xls reader, so it is intentionally excluded (fails here as "unsupported file
// type: xls" instead of later as "malformed file format"). Matching is case-sensitive.
var excelSupportedFileExtensions = map[string]bool{
	"xlsx": true,
	"csv":  true,
}

// Server holds the api layer's dependencies.
type Server struct {
	// Logger mirrors Python's log; nothing is emitted when nil.
	Logger Logger
}

// Logger is the minimal logging interface used by the api layer.
// *slog.Logger (from common/logger) satisfies it directly.
type Logger interface {
	Debug(msg string, args ...any)
	Error(msg string, args ...any)
}

func (s *Server) logError(msg string) {
	if s.Logger != nil {
		s.Logger.Error(msg)
	}
}

func (s *Server) logDebug(msg string) {
	if s.Logger != nil {
		s.Logger.Debug(msg)
	}
}

// Error responses

// HTTPError corresponds to FastAPI's HTTPException.
type HTTPError struct {
	StatusCode int
	Detail     any
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d", e.StatusCode) }

// detailMsg corresponds to Python's detail={"msg": ..., "detail": ...}.
// A struct (not a map) preserves Python's literal key order.
type detailMsg struct {
	Msg    string `json:"msg"`
	Detail string `json:"detail"`
}

// detailError corresponds to Python's detail={"msg": ..., "error": ...}.
type detailError struct {
	Msg   string `json:"msg"`
	Error string `json:"error"`
}

func httpErrMsg(msg, detail string) *HTTPError {
	return &HTTPError{StatusCode: 400, Detail: detailMsg{Msg: msg, Detail: detail}}
}

func httpErrError(msg, errText string) *HTTPError {
	return &HTTPError{StatusCode: 400, Detail: detailError{Msg: msg, Error: errText}}
}

// writeInternalServerError replicates Starlette's default 500 byte-for-byte: body exactly
// "Internal Server Error" (no trailing newline), only content-type and content-length headers.
// http.Error must NOT be used: it appends '\n' and adds X-Content-Type-Options: nosniff.
func writeInternalServerError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte("Internal Server Error"))
}

// writeJSON writes a JSON response; on serialization failure it degrades to a FastAPI-style 500.
// Must use sqlutil.MarshalJSON: json.Marshal HTML-escapes < > &, unlike Python's ORJSONResponse.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := sqlutil.MarshalJSON(v)
	if err != nil {
		writeInternalServerError(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeError maps handler errors to responses: HTTPError -> {"detail":...} with its status,
// ValidationErrors -> 422 {"detail":[...]}, anything else -> 500 "Internal Server Error".
func writeError(w http.ResponseWriter, err error) {
	var he *HTTPError
	if errors.As(err, &he) {
		writeJSON(w, he.StatusCode, map[string]any{"detail": he.Detail})
		return
	}
	var ve *ValidationErrors
	if errors.As(err, &ve) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": ve.Errors})
		return
	}
	writeInternalServerError(w)
}

// Request body parsing

// readJSONBody reads the request body and parses it into T, mirroring FastAPI:
//   - empty body -> 422 missing, loc ["body"], input null;
//   - invalid JSON -> json_invalid (loc index is Python's character index, input {});
//   - model errors come from each request type's UnmarshalJSON, whose locs are relative to
//     the body root; the "body" prefix is added here.
func readJSONBody[T any](r *http.Request) (T, error) {
	var v T
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return v, &ValidationErrors{Errors: []validationError{jsonInvalidError(err, body)}}
	}
	if len(body) == 0 {
		return v, &ValidationErrors{Errors: []validationError{{
			Type: "missing", Loc: []any{"body"}, Msg: msgFieldRequired,
		}}}
	}
	if err := json.Unmarshal(body, &v); err != nil {
		var ve *ValidationErrors
		if errors.As(err, &ve) {
			return v, &ValidationErrors{Errors: relocate(ve.Errors, "body")}
		}
		// Only whole-document syntax errors reach here: field-level errors come from
		// UnmarshalJSON (json.Unmarshal runs checkValid on the whole document first).
		return v, &ValidationErrors{Errors: []validationError{jsonInvalidError(err, body)}}
	}
	return v, nil
}

// jsonInvalidError maps Go JSON syntax errors to FastAPI's json_invalid detail:
// loc=("body", pos), input always {}, ctx.error=msg, pos a Python-style character index.
// Escape errors are rescanned (firstInvalidEscape), truncation is reconstructed
// (eofJSONError), other shapes go through jsonSyntaxMsg; uncovered shapes degrade to Go's
// text. Python 3.13-only wording is not covered (known partial coverage).
func jsonInvalidError(err error, body []byte) validationError {
	pos := utf8.RuneCount(body)
	msg := "Expecting value"
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		goMsg := syn.Error()
		switch {
		case strings.Contains(goMsg, "in string escape code"),
			strings.Contains(goMsg, `in \u hexadecimal character escape`):
			// Go's Offset points past the offending character while Python's pos points at
			// 'u' or the backslash itself; rescan to get Python's position.
			if m, p, ok := firstInvalidEscape(body); ok {
				msg, pos = m, utf8.RuneCount(body[:p])
				break
			}
			fallthrough
		case goMsg == errUnexpectedEOF, strings.Contains(goMsg, "invalid character ' '"):
			// Truncation: Go's scanner has an implicit space sentinel at end of data, so
			// truncated tokens report "invalid character ' '" instead of EOF.
			msg, pos = eofJSONError(body)
		default:
			// Go's Offset is the 1-based byte offset past the bad byte; Python's pos is the
			// 0-based character index of it — count in runes.
			n := int(syn.Offset) - 1
			if n < 0 {
				n = 0
			}
			if n > len(body) {
				n = len(body)
			}
			pos = utf8.RuneCount(body[:n])
			msg = jsonSyntaxMsg(goMsg)
		}
	}
	return validationError{
		Type: "json_invalid", Loc: []any{"body", pos}, Msg: "JSON decode error",
		Input: json.RawMessage("{}"), Ctx: map[string]any{"error": msg},
	}
}

// errUnexpectedEOF is encoding/json's error text for "document ended midway"
// (set by scanner.eof() in checkValid).
const errUnexpectedEOF = "unexpected end of JSON input"

// eofJSONError reconstructs Python's (msg, pos) for a truncated document: a minimal scanner
// walks to the end and reports the expected token's wording (pos = character index). Bad
// literals report "Expecting value" at the token start, not at the end (e.g. `{"a":tru`).
func eofJSONError(body []byte) (string, int) {
	// at converts a byte index into Python's character index.
	at := func(i int) int { return utf8.RuneCount(body[:i]) }

	// States: expecting value / object key / colon / just after a value (',' or closer).
	const (
		stValue = iota
		stKey
		stColon
		stAfter
	)
	var stack []byte // container stack: '{' or '['
	var empty []bool // per container: no key/value read yet (decides whether } or ] may close it directly)
	state := stValue
	i := 0

	push := func(c byte) {
		stack = append(stack, c)
		empty = append(empty, true)
	}
	pop := func() {
		if n := len(stack); n > 0 {
			stack = stack[:n-1]
			empty = empty[:n-1]
		}
	}
	// After a value: the container is no longer empty; next expect a delimiter or closer.
	readValue := func() {
		if n := len(stack); n > 0 {
			empty[n-1] = false
		}
		state = stAfter
	}

	for {
		for i < len(body) && isJSONSpace(body[i]) {
			i++
		}
		if i >= len(body) {
			// End of document: wording depends on the current expectation; pos is the end.
			switch state {
			case stKey:
				return "Expecting property name enclosed in double quotes", at(len(body))
			case stColon:
				return "Expecting ':' delimiter", at(len(body))
			case stAfter:
				return "Expecting ',' delimiter", at(len(body))
			}
			return "Expecting value", at(len(body))
		}
		c := body[i]
		switch state {
		case stValue:
			switch c {
			case '{', '[':
				push(c)
				i++
				if c == '{' {
					state = stKey
				}
			case '"':
				end, msg, pos := scanJSONString(body, i)
				if msg != "" {
					return msg, at(pos)
				}
				i = end
				readValue()
			case '}', ']':
				// Only a just-opened, still-empty container may close directly ({} / []);
				// otherwise Python saw a closer in a value position.
				if n := len(stack); n > 0 && empty[n-1] && closer(stack[n-1]) == c {
					i++
					pop()
					state = stAfter
					continue
				}
				return "Expecting value", at(i)
			default:
				// Literal/number: the longest valid value prefix counts as "value read"; a token
				// with no valid prefix reports Expecting value at its start.
				start := i
				for i < len(body) && !isJSONSpace(body[i]) && !isJSONDelim(body[i]) {
					i++
				}
				if n := longestValidValuePrefix(body[start:i]); n == 0 {
					return "Expecting value", at(start)
				} else {
					i = start + n
				}
				readValue()
			}
		case stKey:
			// An empty {} may close directly while no key has been read yet.
			if c == '}' {
				if n := len(stack); n > 0 && empty[n-1] && stack[n-1] == '{' {
					i++
					pop()
					state = stAfter
					continue
				}
			}
			if c != '"' {
				return "Expecting property name enclosed in double quotes", at(i)
			}
			end, msg, pos := scanJSONString(body, i)
			if msg != "" {
				return msg, at(pos)
			}
			i = end
			if n := len(stack); n > 0 {
				empty[n-1] = false
			}
			state = stColon
		case stColon:
			if c != ':' {
				return "Expecting ':' delimiter", at(i)
			}
			i++
			state = stValue
		case stAfter:
			switch c {
			case ',':
				i++
				state = stValue
				if n := len(stack); n > 0 && stack[n-1] == '{' {
					state = stKey
				}
			case '}', ']':
				i++
				pop()
				state = stAfter
			default:
				return "Expecting ',' delimiter", at(i)
			}
		}
	}
}

// closer returns the closing bracket that pairs with the given opening bracket.
func closer(open byte) byte {
	if open == '{' {
		return '}'
	}
	return ']'
}

// isJSONDelim reports whether c is a structural character separating values (stop condition when scanning literals).
func isJSONDelim(c byte) bool {
	switch c {
	case ',', ':', '{', '}', '[', ']', '"':
		return true
	}
	return false
}

// scanJSONString scans from body[i] (must be '"') to just past the closing quote.
// On error it returns (0, msg, pos): msg is Python's JSONDecodeError.msg, pos the **byte**
// index (caller converts to a character index). Three cases: unterminated string (pos =
// opening quote), invalid \uXXXX (pos = 'u'), other invalid escape (pos = backslash).
func scanJSONString(body []byte, i int) (int, string, int) {
	start := i
	for i++; i < len(body); i++ {
		switch body[i] {
		case '\\':
			if i+1 >= len(body) {
				// Backslash at end of document: the whole string is unterminated.
				return 0, msgUnterminatedString, start
			}
			switch body[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i++ // valid escape; skip the escaped character
			case 'u':
				if !isHex4(body, i+2) {
					return 0, msgInvalidUnicodeEscape, i + 1
				}
				if i+6 == len(body) {
					// Python quirk: when input ends exactly after \uXXXX it reports the escape
					// (pos at 'u'), not "unterminated string".
					return 0, msgInvalidUnicodeEscape, i + 1
				}
				i += 5
			default:
				return 0, msgInvalidEscape, i
			}
		case '"':
			return i + 1, "", 0
		}
	}
	return 0, msgUnterminatedString, start
}

// isHex4 reports whether body[i:i+4] is four hexadecimal digits (Python's \u escape requirement).
func isHex4(body []byte, i int) bool {
	if i+4 > len(body) {
		return false
	}
	for _, c := range body[i : i+4] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// The three string-error messages of Python's json (worded differently from Go's; they become ctx.error).
const (
	msgUnterminatedString   = "Unterminated string starting at"
	msgInvalidUnicodeEscape = `Invalid \uXXXX escape`
	msgInvalidEscape        = `Invalid \escape`
)

// firstInvalidEscape finds the first invalid string escape and returns Python's (msg, pos),
// pos a **byte** index; ok=false when none. Needed because Go's Offset and Python's pos do not
// line up for escape errors.
func firstInvalidEscape(body []byte) (string, int, bool) {
	for i := 0; i < len(body); i++ {
		if body[i] != '"' {
			continue
		}
		end, msg, pos := scanJSONString(body, i)
		if msg == msgUnterminatedString {
			// Unterminated is a truncation-class error; handled by another branch.
			return "", 0, false
		}
		if msg != "" {
			return msg, pos, true
		}
		i = end - 1
	}
	return "", 0, false
}

// longestValidValuePrefix returns the length of the longest prefix of text that is a valid
// JSON value; 0 when none (Python then reports Expecting value at the token start).
func longestValidValuePrefix(text []byte) int {
	for n := len(text); n > 0; n-- {
		if json.Valid(text[:n]) {
			return n
		}
	}
	return 0
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// jsonSyntaxMsg maps Go's syntax-error text to Python's JSONDecodeError.msg.
// Order-sensitive: longer texts are matched first.
func jsonSyntaxMsg(goMsg string) string {
	for _, m := range []struct{ goText, pyText string }{
		{"looking for beginning of object key string", "Expecting property name enclosed in double quotes"},
		{"looking for beginning of value", "Expecting value"},
		{"after object key:value pair", "Expecting ',' delimiter"},
		{"after object key", "Expecting ':' delimiter"},
		{"after array element", "Expecting ',' delimiter"},
		{"after top-level value", "Extra data"},
		{"in string escape code", `Invalid \escape`},
		{"in \\u hexadecimal character escape", `Invalid \uXXXX escape`},
		{"in string literal", "Invalid control character at"},
	} {
		if strings.Contains(goMsg, m.goText) {
			return m.pyText
		}
	}
	// Uncovered shapes: return Go's text as-is (best-effort; see the function doc).
	return goMsg
}

// valid_data_source_config

// validDataSourceConfig corresponds to Python's valid_data_source_config.
// Only a conn_test that **raises** counts as failure; returning False does not.
func (s *Server) validDataSourceConfig(ctx context.Context, cfg DataSourceConfig) (ds.DataSource, error) {
	source, err := newDataSource(cfg)
	if err != nil {
		return nil, err
	}
	if _, err := source.ConnTest(ctx); err != nil {
		return nil, httpErrMsg("Failed to connect to the data source; please check that the connection information is correct", err.Error())
	}
	return source, nil
}

// newDataSource corresponds to the branch-construction part of valid_data_source_config.
func newDataSource(cfg DataSourceConfig) (ds.DataSource, error) {
	switch cfg.Type {
	case "mssql":
		c := cfg.Config.(*MssqlConfig)
		return ds.NewMssql(
			c.Host, c.Port, c.Username, c.Password, c.DBName, c.NSName, c.Encrypt, 30,
		), nil

	case "mysql", "maria":
		c := cfg.Config.(*MysqlConfig)
		return ds.NewMysql(c.Host, c.Port, c.Username, c.Password, c.DBName, false), nil

	case "starrocks":
		c := cfg.Config.(*MysqlConfig)
		return ds.NewStarRocks(c.Host, c.Port, c.Username, c.Password, c.DBName, false), nil

	case "oracle":
		c := cfg.Config.(*OracleConfig)
		schema := ""
		if c.NSName != nil {
			schema = *c.NSName
		}
		return ds.NewOracle(c.Host, c.Port, c.Username, c.Password, c.DBName, schema), nil

	case "postgres":
		c := cfg.Config.(*PostgresConfig)
		// Python's Postgres(...) passes no ssl (default False); schema defaults to "public".
		return ds.NewPostgres(c.Host, c.Port, c.Username, c.Password, c.DBName, c.NSName, false), nil

	case "duckdb":
		return newDuckdbStore(cfg.Config.(*DuckdbConfig))

	case "sqlite":
		c := cfg.Config.(*SqliteConfig)
		return ds.NewSqlite(c.DBPath), nil

	default:
		return nil, httpErrMsg("Unsupported data source type", "unsupported data source type: "+cfg.Type)
	}
}

// newDuckdbStore corresponds to the duckdb branch of valid_data_source_config.
func newDuckdbStore(cfg *DuckdbConfig) (ds.DuckdbStore, error) {
	switch cfg.Store.Type {
	case "local":
		c := cfg.Store.Store.(*DuckdbLocalStoreConfig)
		store, err := ds.NewDuckdbLocalStore(c.DirPath)
		if err != nil {
			return nil, err
		}
		return store, nil

	case "s3":
		c := cfg.Store.Store.(*DuckdbS3StoreConfig)
		// Same as Python: env-var-built store takes precedence when complete; else fall back to config.
		s3ds, err := ds.CreateS3StoreFromEnv(c.Kid)
		if err != nil {
			return nil, err
		}
		if s3ds != nil {
			return s3ds, nil
		}
		store, err := ds.NewDuckdbS3Store(ds.S3StoreOptions{
			Host: c.Host, AccessKey: c.AccessKey, SecretKey: c.SecretKey,
			Bucket: c.Bucket, Region: c.Region, Port: c.Port, UseSSL: c.UseSSL,
		})
		if err != nil {
			return nil, err
		}
		return store, nil

	case "mongo":
		c := cfg.Store.Store.(*DuckdbMongoStoreConfig)
		store, err := ds.NewDuckdbMongoStoreForFastGPT(ds.MongoFastGPTOptions{
			URI: c.URI, Kid: c.Kid, Bucket: c.Bucket, FileIDs: c.FileIDs,
		})
		if err != nil {
			return nil, err
		}
		return store, nil

	default:
		// Unreachable: the discriminated union rejects unknown store.type at parse time.
		return nil, httpErrMsg("Unsupported data source type", "unsupported store type: "+cfg.Store.Type)
	}
}

// The four endpoints using Depends(valid_data_source_config); their request body **is** DataSourceConfig.

func (s *Server) handleConnTest(w http.ResponseWriter, r *http.Request) error {
	cfg, err := readJSONBody[DataSourceConfig](r)
	if err != nil {
		return err
	}
	source, err := s.validDataSourceConfig(r.Context(), cfg)
	if err != nil {
		return err
	}
	res, err := source.ConnTest(r.Context())
	if err != nil {
		return err
	}
	if res {
		writeJSON(w, http.StatusOK, map[string]string{"msg": "ok"})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]string{"msg": "fail"})
	return nil
}

func (s *Server) handleGetMetadata(w http.ResponseWriter, r *http.Request) error {
	cfg, err := readJSONBody[DataSourceConfig](r)
	if err != nil {
		return err
	}
	source, err := s.validDataSourceConfig(r.Context(), cfg)
	if err != nil {
		return err
	}
	m, err := source.Metadata(r.Context())
	if err != nil {
		return httpErrMsg("Failed to fetch database metadata", err.Error())
	}
	writeJSON(w, http.StatusOK, m)
	return nil
}

func (s *Server) handleGetMetadataWithValueExamples(w http.ResponseWriter, r *http.Request) error {
	cfg, err := readJSONBody[DataSourceConfig](r)
	if err != nil {
		return err
	}
	source, err := s.validDataSourceConfig(r.Context(), cfg)
	if err != nil {
		return err
	}
	// Python uses the default parameters value_num=3, value_str_max_length=200.
	m, err := source.MetadataWithValueExamples(r.Context(), 3, 200)
	if err != nil {
		return httpErrMsg("Failed to fetch database metadata", err.Error())
	}
	writeJSON(w, http.StatusOK, m)
	return nil
}

func (s *Server) handleGetServerVersion(w http.ResponseWriter, r *http.Request) error {
	cfg, err := readJSONBody[DataSourceConfig](r)
	if err != nil {
		return err
	}
	source, err := s.validDataSourceConfig(r.Context(), cfg)
	if err != nil {
		return err
	}
	v, err := source.ServerVersion(r.Context())
	if err != nil {
		return httpErrMsg("Failed to fetch database version information", err.Error())
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// sql_query

func (s *Server) handleSQLQuery(w http.ResponseWriter, r *http.Request) error {
	req, err := readJSONBody[SqlQueryRequest](r)
	if err != nil {
		return err
	}
	source, err := s.validDataSourceConfig(r.Context(), req.SourceConfig)
	if err != nil {
		return err
	}
	cols, data, sqlErr := source.ExecuteRawSQL(r.Context(), req.SQL)
	if sqlErr != nil {
		return httpErrMsg("Database query failed", sqlErr.Msg)
	}
	// Python's SqlQueryResponse uses default_factory=list; here we ensure non-nil.
	if cols == nil {
		cols = []string{}
	}
	if data == nil {
		data = [][]any{}
	}
	writeJSON(w, http.StatusOK, SqlQueryResponse{Cols: cols, Data: data})
	return nil
}

// sql_format

func (s *Server) handleSQLFormat(w http.ResponseWriter, r *http.Request) error {
	req, err := readJSONBody[SqlQueryRequest](r)
	if err != nil {
		return err
	}
	// The dialect comes from the config **type**, not the connection's dialect; "maria" and
	// "mssql" are unknown to sqlglot, so their SQL is returned as-is.
	dialect := strings.ToLower(req.SourceConfig.Type)
	out, ok, err := sqlg.FormatSQL(req.SQL, dialect)
	if err != nil {
		// sqlglot ParseError: Python's sql_format has no try/except, so it propagates to a
		// 500 "Internal Server Error" (without logging).
		return err
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"sql": req.SQL})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]string{"sql": out})
	return nil
}

// query_by_nl

func (s *Server) handleQueryByNL(w http.ResponseWriter, r *http.Request) error {
	req, err := readJSONBody[QueryByNLRequest](r)
	if err != nil {
		return err
	}
	if s.Logger != nil {
		// Python logs model_dump_json(ensure_ascii=False, indent=2); MarshalJSONIndent matches
		// (json.Marshal would HTML-escape < > & and produce no indentation).
		if b, err := sqlutil.MarshalJSONIndent(req, "  "); err == nil {
			s.logDebug("query_by_nl: " + string(b))
		}
	}

	source, err := s.validDataSourceConfig(r.Context(), req.SourceConfig)
	if err != nil {
		return err
	}

	// Python runs conn_test **again** here and fetches the version separately; the failure message differs.
	if _, err := source.ConnTest(r.Context()); err != nil {
		s.logError(err.Error())
		return httpErrError("Database connection failed; please check that the connection information is correct", err.Error())
	}
	serverVersion, err := source.ServerVersion(r.Context())
	if err != nil {
		s.logError(err.Error())
		return httpErrError("Database connection failed; please check that the connection information is correct", err.Error())
	}

	generateLLM := chatmodel.NewChatOpenAI(
		req.GenerateSQLLLM.Model,
		req.GenerateSQLLLM.APIKey,
		req.GenerateSQLLLM.BaseURL,
		true,
		req.GenerateSQLLLM.ExtraBody,
	)
	defer func() { _ = generateLLM.Close(r.Context()) }()

	var evaluateLLM chatmodel.ChatModel
	if req.EvaluateSQLLLM != nil {
		ev := chatmodel.NewChatOpenAI(
			req.EvaluateSQLLLM.Model,
			req.EvaluateSQLLLM.APIKey,
			req.EvaluateSQLLLM.BaseURL,
			true,
			req.EvaluateSQLLLM.ExtraBody,
		)
		defer func() { _ = ev.Close(r.Context()) }()
		evaluateLLM = ev
	}

	// Python omits schema_format, so the Agent default "m_schema" applies; "" lets NewAgent fall back.
	ag := agent.NewAgent(source, serverVersion, generateLLM, req.ResultNumLimit, "", 0, evaluateLLM)
	ag.Logger = s.Logger

	res, err := ag.ARun(r.Context(), req.Query, req.RetrievedMetadata, req.Evidence)
	if err != nil {
		s.logError(err.Error())
		return httpErrError(err.Error(), err.Error())
	}

	cols, data := res.Cols, res.Data
	if cols == nil {
		cols = []string{}
	}
	if data == nil {
		data = [][]any{}
	}
	writeJSON(w, http.StatusOK, QueryByNLResponse{
		Answer:       res.Answer,
		SQL:          res.SQL,
		SQLRes:       SqlQueryResponse{Cols: cols, Data: data},
		InputTokens:  res.TotalInputTokens,
		OutputTokens: res.TotalOutputTokens,
	})
	return nil
}

// excel_upload / excel_delete

// excelStoreConfig is the content of the source_config form field for excel_upload / excel_delete.
type excelStoreConfig struct {
	Type      string         `json:"type"`
	Kid       string         `json:"kid"`
	Host      string         `json:"host"`
	Port      int            `json:"port"`
	AccessKey string         `json:"access_key"`
	SecretKey string         `json:"secret_key"`
	Bucket    string         `json:"bucket"`
	Region    string         `json:"region"`
	UseSSL    bool           `json:"use_ssl"`
	URI       string         `json:"uri"`
	Metadata  map[string]any `json:"metadata"`
}

// unsupportedExcelStoreTypeError signals an unsupported store type for excel_upload/delete:
// the write path only covers s3 and mongo (local's insert_data/delete_data are empty implementations).
type unsupportedExcelStoreTypeError struct{ Type string }

func (e *unsupportedExcelStoreTypeError) Error() string {
	return "unsupported excel store type: " + strconv.Quote(e.Type) +
		" (the write path only supports s3 and mongo; the local store's insert_data/delete_data are empty implementations)"
}

// newExcelStore builds the store for excel_upload / excel_delete.
// Intentional divergence: Python routes every non-s3 type into the mongo branch (so type=local
// failed with "kid is not a 24-character ObjectID", masked as "invalid configuration
// information"); here only "mongo" and "" (Python's else default) take the mongo path.
func newExcelStore(raw string) (ds.DuckdbStore, error) {
	cfg := excelStoreConfig{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, err
	}
	switch cfg.Type {
	case "s3":
		s3ds, err := ds.CreateS3StoreFromEnv(cfg.Kid)
		if err != nil {
			return nil, err
		}
		if s3ds != nil {
			return s3ds, nil
		}
		return ds.NewDuckdbS3Store(ds.S3StoreOptions{
			Host: cfg.Host, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey,
			Bucket: cfg.Bucket, Region: cfg.Region, Port: cfg.Port, UseSSL: cfg.UseSSL,
		})

	case "", "mongo":
		// Python: `os.getenv("MONGODB_URI") or cfg["uri"]` — empty env falls back to config.
		uri := os.Getenv("MONGODB_URI")
		if uri == "" {
			uri = cfg.URI
		}
		bucket := os.Getenv("MONGODB_BUCKET")
		if bucket == "" {
			bucket = cfg.Bucket
		}
		return ds.NewDuckdbMongoStoreForFastGPT(ds.MongoFastGPTOptions{
			URI: uri, Kid: cfg.Kid, Bucket: bucket,
		})

	default:
		return nil, &unsupportedExcelStoreTypeError{Type: cfg.Type}
	}
}

// excelUploadResponse corresponds to the dict returned by Python's excel_upload.
// Key order must match Python: msg, rows, cols, filename; file_id only when non-empty.
type excelUploadResponse struct {
	Msg      string `json:"msg"`
	Rows     int    `json:"rows"`
	Cols     int    `json:"cols"`
	Filename string `json:"filename"`
	FileID   string `json:"file_id,omitempty"`
}

func (s *Server) handleExcelUpload(w http.ResponseWriter, r *http.Request) error {
	fileHeader, sourceConfig, err := parseExcelForm(r)
	if err != nil {
		return err
	}

	// Python: `if file.size and file.size > MAX_EXCEL_FILE_SIZE`.
	if fileHeader.Size > 0 && fileHeader.Size > int64(maxExcelFileSize) {
		return httpErrMsg("File upload failed", "file size exceeds the limit")
	}

	if fileHeader.Filename == "" {
		return httpErrMsg("File upload failed", "filename must not be empty")
	}
	filename := pyUnquote(fileHeader.Filename)
	// Python: `filename.split(".")` must yield exactly 2 parts; otherwise the ValueError is
	// uncaught and FastAPI returns 500.
	parts := strings.Split(filename, ".")
	if len(parts) != 2 {
		return fmt.Errorf(
			"ValueError: not enough values to unpack (expected 2, got %d)", len(parts),
		)
	}
	name, extension := parts[0], parts[1]
	if !excelSupportedFileExtensions[extension] {
		return httpErrMsg("File upload failed", "unsupported file type: "+extension)
	}

	// Order matches Python: construct the store first, then read the file (decides which error wins).
	store, err := newExcelStore(sourceConfig)
	if err != nil {
		s.logError(err.Error())
		var unsupported *unsupportedExcelStoreTypeError
		if errors.As(err, &unsupported) {
			// Divergence: Python always reports "invalid configuration information"; we surface
			// the real "unsupported type" reason.
			return httpErrMsg("File upload failed",
				"unsupported data source type: "+unsupported.Type+" (excel writes only support s3 and mongo)")
		}
		return httpErrMsg("File upload failed", "invalid configuration information")
	}

	// Python reads the file only after the store is constructed (inside load_data's argument).
	content, err := io.ReadAll(fileHeader.Open())
	if err != nil {
		s.logError(err.Error())
		return httpErrMsg("File upload failed", "malformed file format, read failed")
	}

	fi := &ds.FileInfo{Name: name, Extension: extension, Data: content}
	if err := store.LoadData(r.Context(), []*ds.FileInfo{fi}); err != nil {
		s.logError(err.Error())
		return httpErrMsg("File upload failed", "malformed file format, read failed")
	}
	rows, cols, err := store.GetNumRowsAndCols(r.Context(), name)
	if err != nil {
		s.logError(err.Error())
		return httpErrMsg("File upload failed", "malformed file format, read failed")
	}

	meta := excelStoreConfigMeta(sourceConfig)
	fileID, err := store.InsertData(r.Context(), filename, bytes.NewReader(content), meta)
	if err != nil {
		s.logError(err.Error())
		return httpErrMsg("File upload failed", err.Error())
	}

	resp := excelUploadResponse{Msg: "success", Rows: rows, Cols: cols, Filename: filename}
	if fileID != "" {
		resp.FileID = fileID
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleExcelDelete(w http.ResponseWriter, r *http.Request) error {
	filename, sourceConfig, err := parseExcelDeleteForm(r)
	if err != nil {
		return err
	}
	store, err := newExcelStore(sourceConfig)
	if err != nil {
		s.logError(err.Error())
		var unsupported *unsupportedExcelStoreTypeError
		if errors.As(err, &unsupported) {
			// Same messages as excel_upload (divergence; see newExcelStore).
			return httpErrMsg("File deletion failed",
				"unsupported data source type: "+unsupported.Type+" (excel writes only support s3 and mongo)")
		}
		return httpErrMsg("File deletion failed", "invalid configuration information")
	}
	if err := store.DeleteData(r.Context(), filename); err != nil {
		s.logError(err.Error())
		return httpErrMsg("File deletion failed", err.Error())
	}
	writeJSON(w, http.StatusOK, map[string]string{"msg": "success", "filename": filename})
	return nil
}

// Form parsing helpers

// excelFormFile is the file part required by excel_upload.
type excelFormFile struct {
	Filename string
	Size     int64
	// Open returns a reader starting from the beginning of the file (mirrors Python's seek(0)).
	Open func() io.Reader
}

// missingBodyField builds the detail entry for a missing form field:
// loc ["body", <field name>], input null.
func missingBodyField(name string) validationError {
	return validationError{Type: "missing", Loc: []any{"body", name}, Msg: msgFieldRequired}
}

func parseExcelForm(r *http.Request) (*excelFormFile, string, error) {
	// Like FastAPI: report **all** missing form fields at once, in signature order (file,
	// source_config). A non-multipart request reports the same two (empty form, not "missing body").
	form := excelForm(r)
	var errs []validationError
	fhs := form.File["file"]
	if len(fhs) == 0 {
		errs = append(errs, missingBodyField("file"))
	}
	if _, ok := form.Value["source_config"]; !ok {
		errs = append(errs, missingBodyField("source_config"))
	}
	if len(errs) > 0 {
		return nil, "", &ValidationErrors{Errors: errs}
	}

	fh := fhs[0]
	return &excelFormFile{
		Filename: fh.Filename,
		Size:     fh.Size,
		Open: func() io.Reader {
			f, err := fh.Open()
			if err != nil {
				return strings.NewReader("")
			}
			return f
		},
	}, form.Value["source_config"][0], nil
}

func parseExcelDeleteForm(r *http.Request) (string, string, error) {
	// Signature order: filename, source_config.
	form := excelForm(r)
	var errs []validationError
	fn, hasFilename := form.Value["filename"]
	if !hasFilename {
		errs = append(errs, missingBodyField("filename"))
	}
	sc, hasSourceConfig := form.Value["source_config"]
	if !hasSourceConfig {
		errs = append(errs, missingBodyField("source_config"))
	}
	if len(errs) > 0 {
		return "", "", &ValidationErrors{Errors: errs}
	}
	return fn[0], sc[0], nil
}

// excelForm reads the multipart form; on parse failure or non-multipart requests it returns an
// empty (non-nil) form, so callers only need to check field presence.
func excelForm(r *http.Request) *multipart.Form {
	if err := r.ParseMultipartForm(32 << 20); err != nil || r.MultipartForm == nil {
		return &multipart.Form{
			Value: map[string][]string{},
			File:  map[string][]*multipart.FileHeader{},
		}
	}
	return r.MultipartForm
}

// excelStoreConfigMeta corresponds to `cfg.get("metadata")`.
func excelStoreConfigMeta(raw string) map[string]any {
	cfg := excelStoreConfig{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil
	}
	return cfg.Metadata
}
