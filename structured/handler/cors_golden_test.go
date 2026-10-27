package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Offline parity test for the CORS/routing layer (404 / 405 / 307 trailing-slash redirects /
// CORS simple + preflight), comparing all response headers and bodies against a golden
// generated from real FastAPI + starlette. The generator script is gone; the golden is read-only.

type corsCase struct {
	Name    string     `json:"name"`
	Method  string     `json:"method"`
	Path    string     `json:"path"`
	Headers [][]string `json:"headers"`
}

type corsGolden struct {
	Name    string            `json:"name"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Runtime-generated headers, implementation-independent: date (server clock) and server
// (ASGI/Go runtime).
var corsIgnoredHeaders = map[string]bool{"date": true, "server": true}

func TestCORSAndRoutingGolden(t *testing.T) {
	cases := loadCORSJSON[[]corsCase](t, "cors_cases.json")
	golden := loadCORSJSON[[]corsGolden](t, "cors_golden.json")

	goldenByName := map[string]corsGolden{}
	for _, g := range golden {
		goldenByName[g.Name] = g
	}

	// Real server, not ResponseRecorder: the Recorder skips net/http's Content-Length
	// computation and HEAD suppression, causing spurious header differences.
	ts := httptest.NewServer(NewHandler(&Server{}))
	defer ts.Close()

	client := &http.Client{
		// The 307 itself is under test; it must not be followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	if len(cases) == 0 {
		t.Fatal("the case set is empty")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			want, ok := goldenByName[c.Name]
			if !ok {
				t.Fatalf("golden is missing case %s", c.Name)
			}
			req, err := http.NewRequest(c.Method, ts.URL+c.Path, nil)
			if err != nil {
				t.Fatalf("failed to construct request: %v", err)
			}
			for _, kv := range c.Headers {
				req.Header.Set(kv[0], kv[1])
			}
			// Match TestClient's fake hostname; otherwise the 307's Location is not comparable.
			req.Host = "testserver"

			res, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("failed to read response body: %v", err)
			}

			if res.StatusCode != want.Status {
				t.Errorf("status code: expected %d, got %d", want.Status, res.StatusCode)
			}
			if got := string(body); got != want.Body {
				t.Errorf("response body: expected %q, got %q", want.Body, got)
			}

			got := map[string]string{}
			for k, v := range res.Header {
				got[strings.ToLower(k)] = strings.Join(v, ", ")
			}
			keys := map[string]bool{}
			for k := range want.Headers {
				keys[k] = true
			}
			for k := range got {
				keys[k] = true
			}
			for k := range keys {
				if corsIgnoredHeaders[k] {
					continue
				}
				pv, pok := want.Headers[k]
				gv, gok := got[k]
				if pok != gok || pv != gv {
					t.Errorf("response header %s: expected %q (present=%v), got %q (present=%v)", k, pv, pok, gv, gok)
				}
			}
		})
	}
}

// loadCORSJSON reads JSON files under testdata.
func loadCORSJSON[T any](t *testing.T, name string) T {
	t.Helper()
	var v T
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("failed to parse %s: %v", path, err)
	}
	return v
}
