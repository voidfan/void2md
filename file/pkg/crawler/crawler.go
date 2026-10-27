package crawler

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"void2md/common/logger"
	"void2md/file/pkg/parse"
)

// log returns the module logger lazily (see file/pkg/service/crawl).
func log() *slog.Logger { return logger.GetLogger("crawler") }

// Crawl does a same-domain BFS crawl starting from opts.URL.
//
// Terminal-state rule (design §2.2, single entry point):
//   - >=1 page fetched → return Result (early stop reflected in Truncated / StopReason)
//   - 0 pages fetched  → return error with a specific readable reason
//     (entry page failed / selector matched nothing / entry content empty)
//
// onProgress is optional; called with the completed page count after each
// page is accepted, as the progress signal for the running state.
func Crawl(ctx context.Context, opts Options, onProgress func(done int)) (*Result, error) {
	entry, err := NormalizeURL(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid entry url: %w", err)
	}

	// Parameter defaults
	if opts.MaxPage <= 0 {
		opts.MaxPage = 1
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.TaskTimeout <= 0 {
		opts.TaskTimeout = 5 * time.Minute
	}

	taskCtx, cancel := context.WithTimeout(ctx, opts.TaskTimeout)
	defer cancel()

	fetcher := NewFetcher(opts.UserAgent, opts.PageTimeout, opts.MaxHTMLSize)

	// These variables are only read/written in the scheduler goroutine: each
	// batch's workers write only to their own results[i], and the scheduler
	// aggregates after wg.Wait(). So **no mutex is needed** — do not apply the
	// design §4.3 wording "visited is a mutex-protected set" here.
	var (
		pages      []Page
		totalBytes int64
		stop       StopReason
		entryErr   error
	)

	// process handles one page: fetch → parse DOM → extract links (whole doc) → scope content → render Markdown.
	process := func(rawURL string) (Page, []string, error) {
		base, err := url.Parse(rawURL)
		if err != nil {
			return Page{}, nil, fmt.Errorf("invalid url: %w", err)
		}
		body, err := fetcher.Fetch(taskCtx, rawURL)
		if err != nil {
			return Page{}, nil, err
		}
		doc, err := html.Parse(bytes.NewReader(body))
		if err != nil {
			return Page{}, nil, fmt.Errorf("html parse failed: %w", err)
		}

		// Links are extracted from the **whole document** (design §4.2 step 10),
		// otherwise nav-bar pagination links are never discovered.
		links := ExtractLinks(doc, base)

		scoped, err := parse.SelectNode(doc, opts.Selector)
		if err != nil {
			return Page{}, links, err
		}
		// Empty-page rule (design §4.2 step 7): covers "selector matched nothing",
		// "matched but no text", and "empty body" in one check.
		if len(scoped) == 0 {
			return Page{}, links, fmt.Errorf("empty content (selector %q matched nothing)", opts.Selector)
		}

		// Bytes were already decoded to UTF-8 in Fetch, so "utf-8" must be passed
		// here, or the parser re-decodes per <meta charset> and produces mojibake.
		md, _, err := parse.ParseHTMLBytesToMarkdownWithCharset(taskCtx, scoped, rawURL, "utf-8")
		if err != nil {
			return Page{}, links, fmt.Errorf("markdown render failed: %w", err)
		}
		if strings.TrimSpace(md) == "" {
			return Page{}, links, fmt.Errorf("empty content (selector %q produced no text)", opts.Selector)
		}
		return Page{URL: rawURL, Title: extractTitle(doc, rawURL), Markdown: md}, links, nil
	}

	// visited must persist across levels: rebuilding per level re-enqueues fetched pages.
	visited := map[string]bool{entry.String(): true}
	frontier := []string{entry.String()}
	for len(frontier) > 0 {
		if taskCtx.Err() != nil {
			stop = StopTimeout
			break
		}
		if len(pages) >= opts.MaxPage {
			stop = StopMaxPage
			break
		}

		batch := frontier
		if remaining := opts.MaxPage - len(pages); len(batch) > remaining {
			batch = batch[:remaining]
			stop = StopMaxPage // frontier has leftovers: truncated by the limit
		}

		type pageResult struct {
			url   string
			page  Page
			links []string
			err   error
		}
		results := make([]pageResult, len(batch))
		sem := make(chan struct{}, opts.Concurrency)
		var wg sync.WaitGroup

		for i, u := range batch {
			wg.Add(1)
			go func(i int, u string) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-taskCtx.Done():
					results[i] = pageResult{url: u, err: taskCtx.Err()}
					return
				}
				p, links, err := process(u)
				results[i] = pageResult{url: u, page: p, links: links, err: err}
			}(i, u)
		}
		wg.Wait()

		next := make([]string, 0, len(batch))
		for _, r := range results {
			// Link enqueueing must happen **before** the err check (design §4.2
			// step 10): on the "selector missed" / "empty content" paths where the
			// page is dropped as empty, process still returns the links it
			// extracted (see the `return Page{}, links, err` sites above). Putting
			// this after `continue` would drop those links wholesale — and
			// nav/aggregator pages are exactly this shape; the cost is a whole
			// missing subtree while the task reports success.
			// (Failed fetches have nil links, so this is a no-op there and does not
			// affect "child-page failure is log-only".)
			for _, l := range r.links {
				// Same-domain filter (design §4.2 step 11). Without it the crawler follows all external links.
				if ShouldEnqueue(l, entry, visited) {
					visited[l] = true
					next = append(next, l)
				}
			}
			if r.err != nil {
				// Child-page failure is log-only (design §5); entry-page failure is recorded as the task-level reason.
				log().Warn("html crawl page skipped", "url", r.url, "reason", r.err.Error())
				if r.url == entry.String() {
					entryErr = r.err
				}
				continue
			}
			// opts.MaxTaskBytes <= 0 means **unlimited** here — intentionally
			// different from the `<=0 → default` rule for MaxPage/Concurrency/
			// TaskTimeout above: this package is an internal library, so callers
			// using a zero-value Options should get "it runs", not "first page
			// already hits the limit". On the production path it is always
			// positive — the service layer uses
			// `config.HTMLCrawlConfig.Normalize()` so a configured 0 is replaced
			// by the default first (PDF2TEXT_HTML_MAX_TASK_BYTES_MB, default
			// 200MB); see final review F6.
			if opts.MaxTaskBytes > 0 && totalBytes+int64(len(r.page.Markdown)) > opts.MaxTaskBytes {
				stop = StopMaxBytes
				break
			}
			pages = append(pages, r.page)
			totalBytes += int64(len(r.page.Markdown))
			if onProgress != nil {
				onProgress(len(pages))
			}
		}

		if stop != StopNone {
			break
		}
		if taskCtx.Err() != nil {
			stop = StopTimeout
			break
		}
		frontier = next
	}

	// Terminal-state rule (design §2.2): 0 pages → error with a specific reason.
	//
	// stop is deliberately checked before entryErr: on task timeout the entry
	// page's err is necessarily "context deadline exceeded" from taskCtx
	// cancellation; checking entryErr first would mask the more specific and
	// actionable "task timed out" reason with a generic request failure,
	// defeating design §2.2's "timeout with 0 pages must say it was a timeout".
	// The entry page's own failure reason is still appended for debugging
	// (e.g. timeout plus a 404 on the entry page).
	if len(pages) == 0 {
		if stop == StopTimeout {
			if entryErr != nil {
				return nil, fmt.Errorf("entry page %s produced no content: task timed out after %s (entry error: %v)",
					entry.String(), opts.TaskTimeout, entryErr)
			}
			return nil, fmt.Errorf("entry page %s produced no content: task timed out after %s",
				entry.String(), opts.TaskTimeout)
		}
		if entryErr != nil {
			return nil, fmt.Errorf("entry page %s failed: %w", entry.String(), entryErr)
		}
		// When the byte budget blocks at the first page, pages stays 0 (budget
		// check breaks **before** append). Without this dedicated branch it would
		// fall through to the generic "produced no content" — ops would go check
		// the site when the real cause is PDF2TEXT_HTML_MAX_TASK_BYTES_MB set too
		// low. Placed after entryErr: when the entry page **itself** fails, that
		// reason is more specific and should surface first.
		if stop == StopMaxBytes {
			return nil, fmt.Errorf("entry page %s produced no content: the first page would already exceed PDF2TEXT_HTML_MAX_TASK_BYTES_MB (%d bytes)",
				entry.String(), opts.MaxTaskBytes)
		}
		return nil, fmt.Errorf("entry page %s produced no content", entry.String())
	}

	return &Result{
		Pages:      pages,
		Truncated:  stop != StopNone || len(frontier) > 0,
		StopReason: stop,
	}, nil
}

// extractTitle picks the title with a three-level fallback: <title> → first <h1> → raw URL (aligned with FastGPT).
func extractTitle(doc *html.Node, fallback string) string {
	if t := strings.TrimSpace(findText(doc, "title")); t != "" {
		return t
	}
	if h := strings.TrimSpace(findText(doc, "h1")); h != "" {
		return h
	}
	return fallback
}

// findText returns the plain text of the first node with the given tag in the subtree.
func findText(doc *html.Node, tag string) string {
	var out string
	var walk func(n *html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == tag {
			out = textContent(n)
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(doc)
	return out
}

// textContent recursively concatenates all text under a node.
func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(m *html.Node)
	walk = func(m *html.Node) {
		if m.Type == html.TextNode {
			b.WriteString(m.Data)
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
