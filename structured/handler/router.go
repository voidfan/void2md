package api

import (
	"net/http"
	"strings"
)

// SetupRoutes sets up the HTTP routes (dbc style: plain paths via mux.HandleFunc, method
// check inside the handler). Paths are relative to the /v1 mount point and registered
// in Python's declaration order.
func (s *Server) SetupRoutes() *http.ServeMux {
	mux, _ := s.setupRoutes()
	return mux
}

// setupRoutes registers all routes and also returns the canonical FULL paths (as the Python
// service sees them) for starletteRouter's redirect/404 checks, which run before stripAPIPrefix.
func (s *Server) setupRoutes() (*http.ServeMux, map[string]bool) {
	mux := http.NewServeMux()
	paths := map[string]bool{}

	const prefix = "/data_source/"
	const fullPrefix = "/v1/data_source/"
	registerPOST(mux, paths, prefix, fullPrefix, "conn_test", s.handleConnTest)
	registerPOST(mux, paths, prefix, fullPrefix, "get_metadata", s.handleGetMetadata)
	registerPOST(mux, paths, prefix, fullPrefix, "get_metadata_with_value_examples", s.handleGetMetadataWithValueExamples)
	registerPOST(mux, paths, prefix, fullPrefix, "get_server_version", s.handleGetServerVersion)
	registerPOST(mux, paths, prefix, fullPrefix, "sql_query", s.handleSQLQuery)
	registerPOST(mux, paths, prefix, fullPrefix, "sql_format", s.handleSQLFormat)
	registerPOST(mux, paths, prefix, fullPrefix, "query_by_nl", s.handleQueryByNL)
	registerPOST(mux, paths, prefix, fullPrefix, "excel_upload", s.handleExcelUpload)
	registerPOST(mux, paths, prefix, fullPrefix, "excel_delete", s.handleExcelDelete)

	return mux, paths
}

// registerPOST registers a POST-only route; handler errors are rendered like FastAPI's
// uncaught exceptions.
func registerPOST(mux *http.ServeMux, paths map[string]bool, prefix, fullPrefix, name string, fn func(w http.ResponseWriter, r *http.Request) error) {
	registerRoute(mux, paths, prefix+name, fullPrefix+name, http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			writeError(w, err)
		}
	})
}

// registerRoute registers path with the method check inside the handler (not the mux), so the
// FastAPI 405 (JSON detail + Allow header) is produced uniformly by methodIs. HEAD is rejected
// too: Python returns 405 Allow: GET for HEAD on GET routes. muxPath is mount-relative; fullPath is
// the original path for starletteRouter's pre-strip checks.
func registerRoute(mux *http.ServeMux, paths map[string]bool, muxPath, fullPath, method string, next http.HandlerFunc) {
	paths[fullPath] = true
	mux.HandleFunc(muxPath, methodIs(method, next))
}

// methodIs rejects other methods with FastAPI's 405 response (JSON detail + Allow header),
// mirroring starlette's per-route method check.
func methodIs(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "Method Not Allowed"})
			return
		}
		next(w, r)
	}
}

// newAPIHandler composes CORS middleware + starlette-compatible dispatch + the ServeMux
// (Python main.py's app assembly).
func newAPIHandler(s *Server) http.Handler {
	mux, paths := s.setupRoutes()
	return corsMiddleware(newStarletteRouter(mux, paths))
}

// NewHandler constructs the standalone http.Handler serving /v1/data_source/*.
// The /v1 prefix is stripped inside the router so the starlette dispatch (trailing-slash 307
// with absolute Location, JSON 404) still sees the original full request path.
func NewHandler(s *Server) http.Handler {
	return newAPIHandler(s)
}

// starletteRouter dispatches like starlette's Router: exact match -> mux (405 via methodIs);
// else the path with trailing slash added/removed (rstrip, all slashes) -> 307 if known; else
// JSON 404 {"detail": "Not Found"}. paths holds canonical FULL paths; the mux holds
// mount-relative patterns reached via stripAPIPrefix. ServeMux's own 404/405/cleanPath
// responses are never used (plain text + nosniff header; all diverge from starlette).
type starletteRouter struct {
	mux   http.Handler
	paths map[string]bool
}

func newStarletteRouter(mux *http.ServeMux, paths map[string]bool) starletteRouter {
	return starletteRouter{mux: stripAPIPrefix(mux), paths: paths}
}

// stripAPIPrefix removes the "/v1" mount prefix when present. Unlike http.StripPrefix,
// paths WITHOUT the prefix pass through unchanged instead of being 404.
func stripAPIPrefix(next http.Handler) http.Handler {
	const prefix = "/v1"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, prefix)
		if p == r.URL.Path {
			next.ServeHTTP(w, r)
			return
		}
		r2 := new(http.Request)
		*r2 = *r
		r2.URL.Path = p
		if r2.URL.RawPath != "" {
			r2.URL.RawPath = strings.TrimPrefix(r2.URL.RawPath, prefix)
		}
		next.ServeHTTP(w, r2)
	})
}

func (sr starletteRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if sr.paths[r.URL.Path] {
		sr.mux.ServeHTTP(w, r)
		return
	}

	if r.URL.Path != "/" {
		alt := r.URL.Path + "/"
		if strings.HasSuffix(r.URL.Path, "/") {
			// starlette rstrips all trailing slashes at once.
			alt = strings.TrimRight(r.URL.Path, "/")
		}
		if sr.paths[alt] {
			writeSlashRedirect(w, r, alt)
			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not Found"})
}

// writeSlashRedirect is starlette's RedirectResponse (307). RedirectResponse sets no
// media_type, so the response has **no Content-Type** — only Content-Length: 0.
func writeSlashRedirect(w http.ResponseWriter, r *http.Request, altPath string) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	loc := scheme + "://" + host + altPath
	if r.URL.RawQuery != "" {
		loc += "?" + r.URL.RawQuery
	}
	w.Header().Set("Location", pyQuote(loc, ":/%#?=@[]!$&'()*+,;="))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusTemporaryRedirect)
}

// ---------------------------------------------------------------------------
// CORS — replicated against starlette 0.50.0 middleware/cors.py, constructed with
// allow_origins/allow_methods/allow_headers all "*" and allow_credentials=True (max_age 600).
// Traps: simple responses mirror the Origin unconditionally whenever one is present (no
// cookie-based condition) plus Vary: Origin; preflight method matching is exact and
// case-sensitive against ALL_METHODS (no upper()).
// ---------------------------------------------------------------------------

// allMethods is cors.py's ALL_METHODS; the order is the one used in the response header.
const allMethods = "DELETE, GET, HEAD, OPTIONS, PATCH, POST, PUT"

// allMethodsTuple is ALL_METHODS for membership checks (exact, case-sensitive).
var allMethodsTuple = []string{"DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"}

// corsMiddleware corresponds to CORSMiddleware.__call__.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin, hasOrigin := headerGet(r, "Origin")
		if !hasOrigin {
			// No Origin header: pass through untouched.
			next.ServeHTTP(w, r)
			return
		}

		// Preflight: OPTIONS carrying Access-Control-Request-Method.
		if r.Method == http.MethodOptions {
			if _, ok := headerGet(r, "Access-Control-Request-Method"); ok {
				writePreflight(w, r, origin)
				return
			}
		}

		// Simple response: headers are injected when the response starts (via a wrapper),
		// so the handler's own Set calls cannot clobber them.
		next.ServeHTTP(&corsSimpleWriter{ResponseWriter: w, origin: origin}, r)
	})
}

// corsSimpleWriter injects the simple-response CORS headers when the response starts
// (cors.py's send()).
type corsSimpleWriter struct {
	http.ResponseWriter
	origin  string
	applied bool
}

func (c *corsSimpleWriter) apply() {
	if c.applied {
		return
	}
	c.applied = true
	h := c.Header()
	// allow_all_origins + allow_credentials: mirror the concrete Origin (last write wins
	// over the "*" that simple_headers would set).
	h.Set("Access-Control-Allow-Origin", c.origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	addVaryHeader(h, "Origin")
}

func (c *corsSimpleWriter) WriteHeader(statusCode int) {
	c.apply()
	c.ResponseWriter.WriteHeader(statusCode)
}

func (c *corsSimpleWriter) Write(b []byte) (int, error) {
	c.apply()
	return c.ResponseWriter.Write(b)
}

// addVaryHeader is MutableHeaders.add_vary_header: appends rather than overwrites.
func addVaryHeader(h http.Header, value string) {
	if existing, ok := headerGetHeader(h, "Vary"); ok {
		value = existing + ", " + value
	}
	h.Set("Vary", value)
}

// writePreflight is cors.py's preflight_response.
func writePreflight(w http.ResponseWriter, r *http.Request, origin string) {
	requestedMethod, _ := headerGet(r, "Access-Control-Request-Method")
	requestedHeaders, hasRequestedHeaders := headerGet(r, "Access-Control-Request-Headers")
	_, hasPrivateNetwork := headerGet(r, "Access-Control-Request-Private-Network")

	// Preflight always mirrors the Origin (allow_all_origins + credentials) with Vary: Origin.
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", allMethods)
	w.Header().Set("Access-Control-Max-Age", "600")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	// allow_all_headers: Allow-Headers is not preset; echo the requested headers as-is
	// (case preserved).
	if hasRequestedHeaders {
		w.Header().Set("Access-Control-Allow-Headers", requestedHeaders)
	}

	// Failure order matches cors.py: method -> private-network.
	failures := []string{}
	if !stringInSlice(allMethodsTuple, requestedMethod) {
		failures = append(failures, "method")
	}
	if hasPrivateNetwork {
		// allow_private_network defaults to False: the header's mere presence fails.
		failures = append(failures, "private-network")
	}

	// PlainTextResponse content type.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if len(failures) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Disallowed CORS " + strings.Join(failures, ", ")))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func stringInSlice(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// headerGet distinguishes "header missing" from "header present but empty" (starlette's
// Headers.get returns None vs ""; Go's Header.Get returns "" for both).
func headerGet(r *http.Request, key string) (string, bool) {
	vs, ok := r.Header[http.CanonicalHeaderKey(key)]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

func headerGetHeader(h http.Header, key string) (string, bool) {
	vs, ok := h[http.CanonicalHeaderKey(key)]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}
