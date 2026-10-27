package oracle

// Service-level end-to-end parity testing: the same request is sent to the real Python
// service (source of truth) and to the Go handler, and responses are diffed field by field.
// Goldens persist to golden/e2e_service.json; regenerate with
// `go test ./tools/oracle -run TestE2EServiceParity` (requires src/dative/main.py).
// Offline mode: DATIVE_E2E_OFFLINE=1, or automatic when the Python source is absent.
// Coverage: sql_format, routing 404/405/redirects, 422 validation, CORS;
// endpoints needing external LLM/database deps are listed in skippedCases.
// Go side defaults to in-process httptest; DATIVE_GO_BASE_URL targets a running service.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"void2md/structured/handler"
)

// silentLogger satisfies api.Server.Logger (only Debug/Error are required).
type silentLogger struct{ t *testing.T }

func (l silentLogger) Debug(msg string, _ ...any) { l.t.Logf("[go debug] %s", msg) }
func (l silentLogger) Error(msg string, _ ...any) { l.t.Logf("[go error] %s", msg) }

type e2eCase struct {
	Name   string
	Method string
	Path   string
	Body   string
	// Headers are extra request headers (Content-Type: application/json is added
	// automatically when Body is non-empty).
	Headers map[string]string
	// CompareHeaders lists response headers (lowercase) to compare; if empty, only
	// status and body are compared.
	CompareHeaders []string
	// Skip, if non-empty, skips the case; the value is the reason (which external
	// dependency is missing).
	Skip string
	// KnownDivergence, if non-empty, marks an intentional divergence: full context is
	// printed, but the case counts as KNOWN rather than FAIL.
	KnownDivergence string
}

const mysqlCfg = `{"type":"mysql","host":"h","username":"u","password":"p","db_name":"d"}`

func jsonBody(sql string) string {
	return `{"source_config":` + mysqlCfg + `,"sql":` + mustJSON(sql) + `}`
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// pureCases are all paths that don't depend on an external LLM/database.
func pureCases() []e2eCase {
	return []e2eCase{
		{Name: "root_404", Method: "GET", Path: "/"},
		{Name: "unknown_path_404", Method: "POST", Path: "/v1/data_source/nope",
			Body: `{}`},
		{Name: "unknown_router_404", Method: "POST", Path: "/v1/nope",
			Body: `{}`},
		{Name: "method_not_allowed_405", Method: "GET", Path: "/v1/data_source/sql_format",
			CompareHeaders: []string{"allow"}},
		{Name: "trailing_slash_redirect", Method: "POST", Path: "/v1/data_source/sql_format/",
			Body: `{}`, CompareHeaders: []string{"location"}},

		{Name: "sql_format_mysql_simple", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: jsonBody("select 1")},
		{Name: "sql_format_mysql_complex", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: jsonBody("SELECT a.id, COUNT(*) c FROM `t_a` a JOIN t_b b ON a.id=b.a_id WHERE a.x IN (1,2,3) AND b.y LIKE '%z%' GROUP BY a.id HAVING c>1 ORDER BY c DESC LIMIT 10")},
		{Name: "sql_format_mysql_case", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: jsonBody("select upper(name) from users where id = 1")},
		{Name: "sql_format_postgres", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"postgres","host":"h","username":"u","password":"p","db_name":"d","schema":"public"},"sql":"select 1"}`},
		{Name: "sql_format_sqlite", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"sqlite","db_path":"/tmp/x.db"},"sql":"select 1"}`},
		{Name: "sql_format_oracle", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"oracle","host":"h","username":"u","password":"p","db_name":"XEPDB1"},"sql":"select 1 from dual"}`},
		{Name: "sql_format_starrocks", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"starrocks","host":"h","username":"u","password":"p","db_name":"d"},"sql":"select 1"}`},
		{Name: "sql_format_duckdb", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"duckdb","store":{"type":"local","dir_path":"/tmp/x"}},"sql":"select 1"}`},
		// mssql/maria aren't in sqlglot.DIALECT_MODULE_NAMES; returned verbatim (original Python behavior).
		{Name: "sql_format_mssql_passthrough", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"mssql","host":"h","username":"u","password":"p","db_name":"d"},"sql":"select   1  as x"}`},
		{Name: "sql_format_maria_passthrough", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"maria","host":"h","username":"u","password":"p","db_name":"d"},"sql":"select   1  as x"}`},
		{Name: "sql_format_empty_sql", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: jsonBody("")},
		// "this is not sql" parses fine in sqlglot (NOT this IS sql); Python returns 200, not 5xx.
		{Name: "sql_format_unparseable", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: jsonBody("this is not sql")},
		{Name: "sql_format_multi_statement", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: jsonBody("select 1; select 2;")},

		// Inputs that truly raise a sqlglot ParseError: Python yields 500 + plain text
		// "Internal Server Error" (no trailing newline); Go must match byte for byte.
		{Name: "sql_format_parse_error_backtick", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"postgres","host":"h","username":"u","password":"p","db_name":"d","schema":"public"},"sql":"select ` + "`x`" + ` from t"}`},
		{Name: "sql_format_parse_error_unmatched_paren", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: jsonBody("select (1")},
		{Name: "sql_format_parse_error_second_stmt", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: jsonBody("select 1; select (")},

		{Name: "validate_empty_body", Method: "POST", Path: "/v1/data_source/sql_format"},
		{Name: "validate_invalid_json", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{oops`},
		{Name: "validate_missing_sql", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":` + mysqlCfg + `}`},
		{Name: "validate_missing_source_config", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"sql":"select 1"}`},
		{Name: "validate_bad_discriminator", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"nope"},"sql":"select 1"}`},
		{Name: "validate_missing_db_name", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"mysql","host":"h","username":"u","password":"p"},"sql":"select 1"}`},
		{Name: "validate_int_parsing", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"mysql","host":"h","port":"abc","username":"u","password":"p","db_name":"d"},"sql":"select 1"}`},
		{Name: "validate_extra_fields_ignored", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":{"type":"mysql","host":"h","username":"u","password":"p","db_name":"d","zzz":1},"sql":"select 1","extra":"x"}`},
		{Name: "validate_sql_wrong_type", Method: "POST", Path: "/v1/data_source/sql_format",
			Body: `{"source_config":` + mysqlCfg + `,"sql":123}`},

		{Name: "cors_simple_404", Method: "GET", Path: "/nope-nope",
			Headers:        map[string]string{"Origin": "http://x.test"},
			CompareHeaders: []string{"access-control-allow-origin", "access-control-allow-credentials", "vary"}},
		{Name: "cors_preflight", Method: "OPTIONS", Path: "/v1/data_source/sql_format",
			Headers: map[string]string{
				"Origin":                         "http://x.test",
				"Access-Control-Request-Method":  "POST",
				"Access-Control-Request-Headers": "content-type",
			},
			CompareHeaders: []string{
				"access-control-allow-origin", "access-control-allow-credentials",
				"access-control-allow-methods", "access-control-allow-headers",
				"access-control-max-age", "vary", "content-type",
			}},
		{Name: "cors_preflight_disallowed_method", Method: "OPTIONS", Path: "/v1/data_source/sql_format",
			Headers: map[string]string{
				"Origin":                        "http://x.test",
				"Access-Control-Request-Method": "TRACE",
			},
			CompareHeaders: []string{"access-control-allow-origin", "vary", "content-type"}},
	}
}

// skippedCases are endpoints requiring external LLM/database deps, explicitly not covered.
func skippedCases() []e2eCase {
	const reason = "requires an external database connection (no data source available in the parity-test environment)"
	const llmReason = "requires an external LLM service (no api_key/base_url available)"
	return []e2eCase{
		{Name: "conn_test", Method: "POST", Path: "/v1/data_source/conn_test", Skip: reason},
		{Name: "get_metadata", Method: "POST", Path: "/v1/data_source/get_metadata", Skip: reason},
		{Name: "get_metadata_with_value_examples", Method: "POST", Path: "/v1/data_source/get_metadata_with_value_examples", Skip: reason},
		{Name: "get_server_version", Method: "POST", Path: "/v1/data_source/get_server_version", Skip: reason},
		{Name: "sql_query", Method: "POST", Path: "/v1/data_source/sql_query", Skip: reason},
		{Name: "query_by_nl", Method: "POST", Path: "/v1/data_source/query_by_nl", Skip: llmReason + "; it also connects to the data source internally first"},
		{Name: "excel_upload", Method: "POST", Path: "/v1/data_source/excel_upload", Skip: reason + "; it also requires a multipart file"},
		{Name: "excel_delete", Method: "POST", Path: "/v1/data_source/excel_delete", Skip: reason},
		// FastAPI auto-generated; no Go equivalent (docs family is out of alignment scope).
		{Name: "openapi_json", Method: "GET", Path: "/openapi.json", Skip: "FastAPI auto-generated OpenAPI docs; no corresponding Go implementation (the coordinator has declared the /docs family out of alignment scope)"},
		{Name: "docs", Method: "GET", Path: "/docs", Skip: "same as above"},
		{Name: "redoc", Method: "GET", Path: "/redoc", Skip: "same as above"},
	}
}

const pythonBaseURL = "http://127.0.0.1:3000"

// pythonSourceExists reports whether the Python implementation is still in the repo
// (it was deleted after the refactor; `git restore src` brings it back). When absent,
// the test falls back to the offline golden instead of skipping, to avoid a false green.
func pythonSourceExists() bool {
	repoRoot := os.Getenv("DATIVE_REPO_ROOT")
	if repoRoot == "" {
		var err error
		repoRoot, err = filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			return false
		}
	}
	_, err := os.Stat(filepath.Join(repoRoot, "src", "dative", "main.py"))
	return err == nil
}

// startPythonService starts the Python service and waits for readiness. Port 3000 is
// hardcoded in src/dative/main.py; DATIVE_E2E_REUSE_PYTHON=1 reuses an already-running
// service instead of failing on a busy port.
func startPythonService(t *testing.T) {
	t.Helper()
	if reached(t, pythonBaseURL+"/healthz", 500*time.Millisecond) {
		if os.Getenv("DATIVE_E2E_REUSE_PYTHON") == "1" {
			t.Logf("reusing the already-running Python service: %s", pythonBaseURL)
			return
		}
		t.Fatalf("port 3000 is already in use: the Python service's listening port is hardcoded to 3000 in main.py. " +
			"Stop the process occupying it first, or set DATIVE_E2E_REUSE_PYTHON=1 to reuse the already-running service.")
	}

	repoRoot := os.Getenv("DATIVE_REPO_ROOT")
	if repoRoot == "" {
		// The test's working directory is tools/oracle.
		var err error
		repoRoot, err = filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			t.Fatalf("failed to resolve the repository root: %v", err)
		}
	}
	srcDir := filepath.Join(repoRoot, "src")
	refDir := filepath.Join(repoRoot, "..", ".sqlglot_ref")

	// Defensive check: callers should have fallen back to the offline golden already.
	if !pythonSourceExists() {
		t.Fatalf("the Python implementation does not exist (%s); cannot collect goldens live; "+
			"the caller should have fallen back to the offline golden first", filepath.Join(srcDir, "dative", "main.py"))
	}

	py := os.Getenv("DATIVE_E2E_PYTHON")
	if py == "" {
		py = "python"
	}
	cmd := exec.Command(py, "-m", "dative.main")
	cmd.Dir = srcDir
	env := os.Environ()
	sep := string(os.PathListSeparator)
	env = append(env, "PYTHONPATH="+srcDir+sep+refDir)
	cmd.Env = env

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start the Python service (%s -m dative.main): %v", py, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("Python service output:\n%s", out.String())
		}
	})

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if reached(t, pythonBaseURL+"/healthz", 500*time.Millisecond) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the Python service to become ready (%s). Output:\n%s", pythonBaseURL, out.String())
}

// reached probes whether url is reachable (any HTTP response counts as reachable).
func reached(t *testing.T, url string, timeout time.Duration) bool {
	t.Helper()
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

type httpResult struct {
	Status  int
	Headers map[string]string // keys are lowercase
	Body    string
}

func doRequest(t *testing.T, baseURL string, c e2eCase) httpResult {
	t.Helper()
	req, err := http.NewRequest(c.Method, baseURL+c.Path, strings.NewReader(c.Body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	if c.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		// Don't follow redirects: the trailing-slash 307 must be inspected raw.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s failed: %v", c.Method, baseURL+c.Path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	h := map[string]string{}
	for k := range resp.Header {
		h[strings.ToLower(k)] = resp.Header.Get(k)
	}
	return httpResult{Status: resp.StatusCode, Headers: h, Body: string(body)}
}

type goldenFile struct {
	Note  string         `json:"note"`
	Cases []goldenRecord `json:"cases"`
}

type goldenRecord struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Body    string            `json:"request_body,omitempty"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Resp    string            `json:"response_body"`
}

func goldenPath() string { return filepath.Join("golden", "e2e_service.json") }

func saveGolden(t *testing.T, recs []goldenRecord) {
	t.Helper()
	g := goldenFile{
		Note: "由 tools/oracle/e2e_test.go 在运行期向真实 Python 服务（python -m dative.main）发起请求后落盘，" +
			"不是手写的期望值。重新生成：go test ./tools/oracle -run TestE2EServiceParity",
		Cases: recs,
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatalf("failed to serialize golden: %v", err)
	}
	if err := os.MkdirAll("golden", 0o755); err != nil {
		t.Fatalf("failed to create golden directory: %v", err)
	}
	if err := os.WriteFile(goldenPath(), append(b, '\n'), 0o644); err != nil {
		t.Fatalf("failed to write golden: %v", err)
	}
}

func loadGolden(t *testing.T) []goldenRecord {
	t.Helper()
	b, err := os.ReadFile(goldenPath())
	if err != nil {
		t.Fatalf("failed to read golden (offline mode requires %s to exist): %v", goldenPath(), err)
	}
	var g goldenFile
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("failed to parse golden: %v", err)
	}
	return g.Cases
}

// normalizeBaseURL replaces base with a {{BASE}} placeholder: the two sides listen on
// different ports, so Location headers must be normalized before comparison.
func normalizeBaseURL(s string, base string) string {
	return strings.ReplaceAll(s, base, "{{BASE}}")
}

// decodeJSON parses with UseNumber so numeric literals aren't rearranged by float64.
func decodeJSON(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	// Only a single JSON value is allowed
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("response body contains trailing content")
	}
	return v, nil
}

// jsonDiff recursively compares two JSON values, appending `path: want vs got` diffs to out.
func jsonDiff(path string, want, got any, out *[]string) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: want object, got %s", path, typeName(got)))
			return
		}
		keys := map[string]bool{}
		for k := range w {
			keys[k] = true
		}
		for k := range g {
			keys[k] = true
		}
		for _, k := range sortedKeys(keys) {
			wv, inW := w[k]
			gv, inG := g[k]
			switch {
			case inW && !inG:
				*out = append(*out, fmt.Sprintf("%s.%s: missing in Go (Python = %s)", path, k, compactJSON(wv)))
			case !inW && inG:
				*out = append(*out, fmt.Sprintf("%s.%s: extra in Go (Go = %s)", path, k, compactJSON(gv)))
			default:
				jsonDiff(path+"."+k, wv, gv, out)
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: want array, got %s", path, typeName(got)))
			return
		}
		if len(w) != len(g) {
			*out = append(*out, fmt.Sprintf("%s: array length want %d, got %d (Python = %s; Go = %s)",
				path, len(w), len(g), compactJSON(want), compactJSON(got)))
		}
		for i := 0; i < len(w) && i < len(g); i++ {
			jsonDiff(fmt.Sprintf("%s[%d]", path, i), w[i], g[i], out)
		}
	default:
		if typeName(want) != typeName(got) || compactJSON(want) != compactJSON(got) {
			*out = append(*out, fmt.Sprintf("%s: want %s, got %s", path, compactJSON(want), compactJSON(got)))
		}
	}
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func sortedKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<marshal error: %v>", err)
	}
	return string(b)
}

func prettyBody(s string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return s + "\n(invalid JSON, printed verbatim)"
	}
	return buf.String()
}

func headerLines(h map[string]string, want []string) string {
	var b strings.Builder
	for _, k := range want {
		v, ok := h[strings.ToLower(k)]
		if !ok {
			v = "<absent>"
		}
		fmt.Fprintf(&b, "  %s: %s\n", k, v)
	}
	return b.String()
}

func formatFailure(c e2eCase, py, gores httpResult, diffs []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== case %s\n", c.Name)
	fmt.Fprintf(&b, "--- request ---\n%s %s\n", c.Method, c.Path)
	if c.Body != "" {
		fmt.Fprintf(&b, "body: %s\n", c.Body)
	}
	for k, v := range c.Headers {
		fmt.Fprintf(&b, "header %s: %s\n", k, v)
	}

	fmt.Fprintf(&b, "--- Python(golden) status=%d ---\n", py.Status)
	b.WriteString(headerLines(py.Headers, c.CompareHeaders))
	b.WriteString(prettyBody(py.Body))
	b.WriteString("\n")

	fmt.Fprintf(&b, "--- Go status=%d ---\n", gores.Status)
	b.WriteString(headerLines(gores.Headers, c.CompareHeaders))
	b.WriteString(prettyBody(gores.Body))
	b.WriteString("\n")

	b.WriteString("--- field diffs ---\n")
	for _, d := range diffs {
		b.WriteString("  " + d + "\n")
	}
	return b.String()
}

// compare returns difference descriptions; empty means the two sides match.
func compare(c e2eCase, py, gores httpResult, pyBase, goBase string) []string {
	var diffs []string

	if py.Status != gores.Status {
		diffs = append(diffs, fmt.Sprintf("status: want %d, got %d", py.Status, gores.Status))
	}

	for _, h := range c.CompareHeaders {
		want := normalizeBaseURL(py.Headers[strings.ToLower(h)], pyBase)
		got := normalizeBaseURL(gores.Headers[strings.ToLower(h)], goBase)
		if want != got {
			diffs = append(diffs, fmt.Sprintf("header %s: want %q, got %q", h, want, got))
		}
	}

	pyNorm := normalizeBaseURL(py.Body, pyBase)
	goNorm := normalizeBaseURL(gores.Body, goBase)
	wantJSON, wErr := decodeJSON(pyNorm)
	gotJSON, gErr := decodeJSON(goNorm)
	switch {
	case wErr == nil && gErr == nil:
		jsonDiff("$", wantJSON, gotJSON, &diffs)
	case wErr != nil && gErr != nil:
		if pyNorm != goNorm {
			diffs = append(diffs, fmt.Sprintf("body: non-JSON and mismatched\n    want %q\n    got  %q", pyNorm, goNorm))
		}
	case wErr != nil:
		diffs = append(diffs, fmt.Sprintf("body: Python non-JSON (%v), Go is JSON: %s", wErr, compactJSON(gotJSON)))
	default:
		diffs = append(diffs, fmt.Sprintf("body: Python is JSON, Go non-JSON (%v): %q", gErr, goNorm))
	}

	// Byte-for-byte criterion: jsonDiff ignores object key order, so "different order, same
	// semantics" (pydantic insertion order vs Go lexicographic) would be a false green.
	// Reported only when the JSON comparison already passed, to avoid drowning its richer diff.
	if len(diffs) == 0 && pyNorm != goNorm {
		diffs = append(diffs, fmt.Sprintf(
			"body differs byte for byte (JSON deep comparison passed, so it's only a key-order/whitespace/escaping difference)\n    want %s\n    got  %s",
			pyNorm, goNorm))
	}

	return diffs
}

// TestE2EServiceParity is the main entry point for service-level parity testing.
//
//	go test ./tools/oracle -run TestE2EServiceParity -v
//
// Environment variables:
//
//	DATIVE_E2E_REUSE_PYTHON=1  reuse a Python service already running on port 3000
//	DATIVE_E2E_OFFLINE=1       don't start Python; use golden/e2e_service.json directly
//	DATIVE_GO_BASE_URL=...     hit a real, running Go service instead of in-process httptest
//	DATIVE_E2E_PYTHON=...      path to the python executable (defaults to python on PATH)
//
// With none set: live golden collection if src/dative/main.py exists, otherwise automatic
// fallback to the offline golden. Either way all cases really run; there is no silent skip.
func TestE2EServiceParity(t *testing.T) {
	cases := pureCases()

	offline := os.Getenv("DATIVE_E2E_OFFLINE") == "1"
	// No Python source → fall back to the offline golden instead of skipping (a silent skip
	// would be a false green). If the golden is missing too, loadGolden fatals.
	if !offline && !pythonSourceExists() {
		offline = true
		t.Logf("Python implementation removed; cannot collect goldens live; automatically switching to the offline golden: %s", goldenPath())
	}

	var golden map[string]goldenRecord
	if offline {
		golden = map[string]goldenRecord{}
		for _, r := range loadGolden(t) {
			golden[r.Name] = r
		}
		t.Logf("offline mode: using the Python responses recorded in %s as golden", goldenPath())
	} else {
		startPythonService(t)
		golden = map[string]goldenRecord{}
		var recs []goldenRecord
		for _, c := range cases {
			res := doRequest(t, pythonBaseURL, c)
			rec := goldenRecord{
				Name: c.Name, Method: c.Method, Path: c.Path, Body: c.Body,
				Status: res.Status, Headers: res.Headers, Resp: res.Body,
			}
			golden[c.Name] = rec
			recs = append(recs, rec)
		}
		saveGolden(t, recs)
		t.Logf("golden generation complete: %d cases, persisted to %s", len(recs), goldenPath())
	}

	// Go side: in-process httptest by default (same NewHandler as cmd/dative);
	// DATIVE_GO_BASE_URL targets a real, separately started service.
	goBase := os.Getenv("DATIVE_GO_BASE_URL")
	goSrv := (*httptest.Server)(nil)
	if goBase == "" {
		goSrv = httptest.NewServer(api.NewHandler(&api.Server{Logger: silentLogger{t: t}}))
		t.Cleanup(goSrv.Close)
		goBase = goSrv.URL
		t.Logf("Go service: in-process httptest %s", goBase)
	} else {
		t.Logf("Go service: external %s", goBase)
	}
	var failed, known int
	for _, c := range cases {
		rec, ok := golden[c.Name]
		if !ok {
			t.Errorf("case %s is missing from the golden (golden and case list are out of sync; please regenerate)", c.Name)
			continue
		}
		pyRes := httpResult{Status: rec.Status, Headers: rec.Headers, Body: rec.Resp}
		goRes := doRequest(t, goBase, c)
		diffs := compare(c, pyRes, goRes, pythonBaseURL, goBase)
		if len(diffs) == 0 {
			t.Logf("PASS %s", c.Name)
			continue
		}
		report := formatFailure(c, pyRes, goRes, diffs)
		if c.KnownDivergence != "" {
			known++
			t.Logf("KNOWN %s (known divergence: %s)\n%s", c.Name, c.KnownDivergence, report)
			continue
		}
		failed++
		t.Errorf("FAIL %s\n%s", c.Name, report)
	}

	// Log the explicitly uncovered endpoints.
	for _, c := range skippedCases() {
		t.Logf("SKIP %s %s %s — %s", c.Method, c.Path, c.Name, c.Skip)
	}

	t.Logf("summary: %d cases, %d matched, %d mismatched, %d known divergences, %d skipped",
		len(cases), len(cases)-failed-known, failed, known, len(skippedCases()))
}
