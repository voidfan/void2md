package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestFetcher_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "test-agent" {
			t.Errorf("User-Agent = %q, want test-agent", got)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><p>hi</p></body></html>"))
	}))
	defer srv.Close()

	f := NewFetcher("test-agent", 5*time.Second, 1<<20)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if !strings.Contains(string(body), "<p>hi</p>") {
		t.Errorf("body = %q", body)
	}
	// text/html; charset=utf-8 takes the "return as-is" branch; the body must remain valid UTF-8.
	// Content inclusion is asserted above; this only adds encoding validity.
	if !utf8.Valid(body) {
		t.Errorf("body declared as utf-8 must stay valid UTF-8, got %q", body)
	}
}

// TestFetcher_CharsetHintWins: when the HTTP header declares gbk, the body must be
// decoded as gbk here, not re-decoded downstream per <meta> (which yields mojibake).
func TestFetcher_CharsetHintWins(t *testing.T) {
	// Body is GBK-encoded Chinese; <meta> says gbk; HTTP header also says gbk.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		enc, _ := simplifiedchinese.GBK.NewEncoder().Bytes(
			[]byte(`<html><head><meta charset="gbk"></head><body><p>中文内容</p></body></html>`))
		_, _ = w.Write(enc)
	}))
	defer srv.Close()

	f := NewFetcher("test-agent", 5*time.Second, 1<<20)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if !utf8.Valid(body) {
		t.Fatalf("decoded body must be valid UTF-8, got %q", body)
	}
	if !strings.Contains(string(body), "中文内容") {
		t.Errorf("gbk content should be decoded to UTF-8, got %q", body)
	}
}

// TestFetcher_HeaderWinsOverLyingMeta pins the **priority** of the encoding fallback chain:
// HTTP header declares gbk (true and decodable), page <meta> lies with big5.
// The Chinese text must decode per the header.
//
// Unlike TestFetcher_CharsetHintWins, the two declarations **conflict** there
// both say gbk, so either winning gives the same result and priority bugs go
// undetected. "Header true, meta lying" is the reason this feature exists:
// letting meta win garbles or breaks the whole page. (A Task 3 review mutation
// where a decodable hint was overridden by meta stayed green across all 96
// cases; only this conflict case catches it.)
func TestFetcher_HeaderWinsOverLyingMeta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		// Body is GBK bytes, but meta falsely claims big5
		enc, _ := simplifiedchinese.GBK.NewEncoder().Bytes(
			[]byte(`<html><head><meta charset="big5"></head><body><p>中文内容</p></body></html>`))
		_, _ = w.Write(enc)
	}))
	defer srv.Close()

	f := NewFetcher("ua", 5*time.Second, 1<<20)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if !strings.Contains(string(body), "中文内容") {
		t.Errorf("header-declared gbk must win over the lying <meta charset=\"big5\">, got %q", body)
	}
}

// TestFetcher_GBKFallback covers the last two steps of the design §4.2 step 5 fallback chain:
// when neither header nor <meta> declares a charset and the bytes are not valid UTF-8, fall back to GBK.
func TestFetcher_GBKFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately plain text/html without charset; the page has no <meta charset> either
		w.Header().Set("Content-Type", "text/html")
		enc, _ := simplifiedchinese.GBK.NewEncoder().Bytes(
			[]byte(`<html><body><p>中文内容</p></body></html>`))
		_, _ = w.Write(enc)
	}))
	defer srv.Close()

	f := NewFetcher("ua", 5*time.Second, 1<<20)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if !utf8.Valid(body) {
		t.Fatalf("decoded body must be valid UTF-8, got %q", body)
	}
	if !strings.Contains(string(body), "中文内容") {
		t.Errorf("undeclared gbk content should fall back to gbk, got %q", body)
	}
}

func TestFetcher_NonHTMLErrors(t *testing.T) {
	tests := []struct {
		name string
		hdr  string
		want string
	}{
		{"pdf", "application/pdf", "is not html"},
		{"plain", "text/plain", "is not html"},
		{"json", "application/json", "is not html"},
		// Empty Content-Type: mime.ParseMediaType errors, so isHTMLContentType takes
		// the "split media type on ;" fallback branch. This case pins that branch is hit.
		{"empty", "", "is not html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.hdr == "" {
					// Must send a real empty string: with no Content-Type, net/http sniffs
					// ("x" becomes text/plain), which would bypass the fallback branch.
					w.Header()["Content-Type"] = []string{""}
				} else {
					w.Header().Set("Content-Type", tt.hdr)
				}
				_, _ = w.Write([]byte("x"))
			}))
			defer srv.Close()

			f := NewFetcher("ua", 5*time.Second, 1<<20)
			_, err := f.Fetch(context.Background(), srv.URL)
			if err == nil {
				t.Fatal("expected error for non-html content type")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

// TestFetcher_AcceptsXHTML pins the paired contract of Fetch: the request sends
// Accept: text/html,application/xhtml+xml, so isHTMLContentType must equally accept
// application/xhtml+xml. Accepting only text/html would break fetching whole XHTML sites.
// Both with and without charset must work (bare media type takes the other charset branch).
func TestFetcher_AcceptsXHTML(t *testing.T) {
	const bodyText = `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><body><p>xhtml body</p></body></html>`

	for _, ct := range []string{"application/xhtml+xml", "application/xhtml+xml; charset=utf-8"} {
		t.Run(ct, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// We advertise accepting xhtml, so the implementation must accept it — the two must match.
				if accept := r.Header.Get("Accept"); !strings.Contains(accept, "application/xhtml+xml") {
					t.Errorf("Accept = %q, want it to include application/xhtml+xml", accept)
				}
				w.Header().Set("Content-Type", ct)
				_, _ = w.Write([]byte(bodyText))
			}))
			defer srv.Close()

			f := NewFetcher("ua", 5*time.Second, 1<<20)
			body, err := f.Fetch(context.Background(), srv.URL)
			if err != nil {
				t.Fatalf("Content-Type %q must be accepted, error = %v", ct, err)
			}
			if !strings.Contains(string(body), "<p>xhtml body</p>") {
				t.Errorf("body = %q, want it to contain the original markup", body)
			}
		})
	}
}

func TestFetcher_StatusAndSizeErrors(t *testing.T) {
	t.Run("404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()
		f := NewFetcher("ua", 5*time.Second, 1<<20)
		_, err := f.Fetch(context.Background(), srv.URL)
		if err == nil || !strings.Contains(err.Error(), "http status 404") {
			t.Errorf("error = %v, want http status 404", err)
		}
	})

	t.Run("oversize", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(strings.Repeat("a", 5000)))
		}))
		defer srv.Close()
		f := NewFetcher("ua", 5*time.Second, 1024) // limit 1KB, response 5KB
		_, err := f.Fetch(context.Background(), srv.URL)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("error = %v, want size-exceeded", err)
		}
	})

	// Boundary: a body **exactly** equal to maxHTMLSize must succeed. The oversize
	// case above (5000 vs 1024) cannot pin the boundary — with `len(body) >=
	// f.maxHTMLSize`, an exactly-at-limit page would be wrongly rejected while the
	// oversize case still passes (same for removing the `+1` in LimitReader).
	t.Run("exactly at limit", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(strings.Repeat("a", 1024)))
		}))
		defer srv.Close()
		f := NewFetcher("ua", 5*time.Second, 1024) // limit 1KB, response exactly 1KB
		body, err := f.Fetch(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("body exactly at maxHTMLSize must be accepted, error = %v", err)
		}
		if len(body) != 1024 {
			t.Errorf("len(body) = %d, want the full 1024 bytes", len(body))
		}
	})
}

func TestFetcher_TooManyRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/next", http.StatusFound)
	}))
	defer srv.Close()

	f := NewFetcher("ua", 5*time.Second, 1<<20)
	_, err := f.Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected redirect error")
	}
	// Assert the constant value itself, not just "redirect-related": http.Client's
	// default policy (10 hops) also returns "stopped after 10 redirects", and with
	// only "redirect" asserted, deleting CheckRedirect entirely still passes —
	// maxRedirects would go untested.
	if !strings.Contains(err.Error(), "stopped after 5") {
		t.Errorf("error = %q, want the cap in maxRedirects (got the default 10?)", err)
	}
}

// TestFetcher_RedirectCapBoundary pins the **boundary value** of the cap: exactly 5 hops must succeed.
//
// Why needed: `via` in `CheckRedirect` **includes the original request**, so
// `len(via) >= 5` actually allows only **4** hops — one short of the "at most 5"
// promised in design §4.2 step 3 and the constant's comment.
// TestFetcher_TooManyRedirects above uses an infinite-redirect site where 4 vs 5
// hops is indistinguishable, so the off-by-one is invisible across the suite
// (both it and "policy deleted entirely" surface as "it terminates"; only the
// latter is caught without this test).
func TestFetcher_RedirectCapBoundary(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n > 0 {
			http.Redirect(w, r, srv.URL+"/?n="+strconv.Itoa(n-1), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>ok</p></body></html>"))
	}))
	defer srv.Close()

	f := NewFetcher("ua", 5*time.Second, 1<<20)

	// Exactly 5 hops: the design-promised limit, must succeed
	if _, err := f.Fetch(context.Background(), srv.URL+"/?n=5"); err != nil {
		t.Errorf("5 redirects must succeed (design §4.2 step 3 limit), error = %v", err)
	}
	// 6 hops: must be rejected
	if _, err := f.Fetch(context.Background(), srv.URL+"/?n=6"); err == nil {
		t.Error("6 redirects must be rejected by maxRedirects")
	}
}
