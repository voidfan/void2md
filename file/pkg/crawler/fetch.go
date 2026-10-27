package crawler

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"void2md/file/pkg/parse"
)

// maxRedirects is the per-page redirect limit, matching design §4.2 step 3
// ("at most 5 redirects"); exceeding it reports "stopped after 5 redirects"
// (wording aligned with net/http's default policy).
//
// **The check is `len(via) > maxRedirects`, not `>=`**: `via` in
// `CheckRedirect` **includes the original request**, so at the k-th redirect
// `len(via) == k`; `>=` would only allow 4 hops, one short of the constant's
// name and the design promise (4 vs 5 hops both surface as "site never
// terminates", so tests rarely cover it naturally — hence noted here).
// **Relative Location headers are resolved by net/http's Client**
// (`resp.Location()` resolves against the current URL); no manual handling.
const maxRedirects = 5

// Fetcher fetches a single page: retrieves raw bytes, validates Content-Type,
// and decodes to UTF-8 using the charset from the HTTP Content-Type.
type Fetcher struct {
	client      *http.Client
	maxHTMLSize int64
	userAgent   string
}

// NewFetcher builds a fetcher. maxHTMLSize <=0 means 10MB.
func NewFetcher(userAgent string, pageTimeout time.Duration, maxHTMLSize int64) *Fetcher {
	if maxHTMLSize <= 0 {
		maxHTMLSize = 10 << 20
	}
	if pageTimeout <= 0 {
		pageTimeout = 30 * time.Second
	}
	return &Fetcher{
		client: &http.Client{
			Timeout: pageTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// via includes the original request: len(via) == k at the k-th redirect, so > allows 5 hops.
				if len(via) > maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
		maxHTMLSize: maxHTMLSize,
		userAgent:   userAgent,
	}
}

// Fetch retrieves a single page, returning HTML bytes decoded to UTF-8.
// The error text is exposed as the task-level error reason (design §5),
// so it must be specific and readable.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request failed: %w", err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !isHTMLContentType(ct) {
		return nil, fmt.Errorf("content-type %q is not html", ct)
	}

	// Read 1 extra byte to distinguish "exactly at the limit" from "over the limit".
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxHTMLSize+1))
	if err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}
	if int64(len(body)) > f.maxHTMLSize {
		return nil, fmt.Errorf("html size exceeds %d bytes", f.maxHTMLSize)
	}

	// Encoding fallback chain (design §4.2 step 5): HTTP Content-Type charset
	// → <meta charset> → utf8.Valid → GBK fallback. DecodeHTMLBytes handles the
	// first two (hint wins, else <meta>). After decoding, downstream must call
	// the parser with "utf-8", or it re-decodes per <meta> and produces mojibake.
	decoded, err := parse.DecodeHTMLBytes(body, charsetFromContentType(ct))
	if err != nil {
		log().Warn("html charset decode failed, using raw bytes", "url", rawURL, "error", err)
		decoded = body
	}

	// Last two steps: when neither header nor <meta> declares a charset,
	// DecodeHTMLBytes returns the bytes as-is. If they are not valid UTF-8,
	// fall back to GBK — otherwise Chinese sites would render as mojibake.
	if !utf8.Valid(decoded) {
		gbk, gerr := simplifiedchinese.GBK.NewDecoder().Bytes(decoded)
		if gerr == nil && utf8.Valid(gbk) {
			log().Warn("html bytes not valid utf-8, decoded as gbk", "url", rawURL)
			decoded = gbk
		} else {
			log().Warn("html bytes not valid utf-8 and gbk fallback failed, using raw bytes", "url", rawURL)
		}
	}
	return decoded, nil
}

// isHTMLContentType reports whether Content-Type is parseable HTML.
func isHTMLContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	}
	return mt == "text/html" || mt == "application/xhtml+xml"
}

// charsetFromContentType extracts the charset param from Content-Type; empty if none.
func charsetFromContentType(ct string) string {
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return params["charset"]
}
