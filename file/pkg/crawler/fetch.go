package crawler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"void2md/file/pkg/parse"
)

// maxRedirects 单页允许跟随的**重定向跳数**上限，与设计 §4.2 第 3 步的
// 「最多 5 跳重定向」一致；超出时报 "stopped after 5 redirects"
// （对齐 net/http 默认策略的措辞）。
//
// **判据是 `len(via) > maxRedirects`，不是 `>=`**：`CheckRedirect` 的 `via`
// **含最初那次请求**，所以第 k 次重定向时 `len(via) == k`；写 `>=` 只放行 4 跳，
// 与常量名和设计承诺差一跳（4 跳与 5 跳在「站点会终止」上是同一种表现，测试很难
// 自然覆盖到，故在此写明）。**相对 Location 由 net/http 的 Client 自行解析**
// （`resp.Location()` 会基于当前 URL 解析），无需手写。
const maxRedirects = 5

// Fetcher 单页抓取器：负责取回原始字节、校验 Content-Type、
// 按 HTTP Content-Type 的 charset 解码为 UTF-8。
type Fetcher struct {
	client      *http.Client
	maxHTMLSize int64
	userAgent   string
}

// NewFetcher 构造抓取器。maxHTMLSize <=0 时视为 10MB。
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
				// via 含最初那次请求：第 k 次重定向时 len(via) == k，故用 > 才能放行 5 跳。
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

// Fetch 抓取单页，返回解码为 UTF-8 的 HTML 字节。
// 返回的 error 文本会作为任务级错误原因对外暴露（设计 §5），故需具体可读。
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

	// 多读 1 字节以区分「刚好达到上限」与「超过上限」
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxHTMLSize+1))
	if err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}
	if int64(len(body)) > f.maxHTMLSize {
		return nil, fmt.Errorf("html size exceeds %d bytes", f.maxHTMLSize)
	}

	// 编码回退链（设计 §4.2 第 5 步）：HTTP Content-Type charset → <meta charset>
	// → utf8.Valid → GBK 兜底。前两步由 DecodeHTMLBytes 完成（hint 优先，空则看 <meta>）。
	// 解码后下游必须以 "utf-8" 调用解析器，否则会按 <meta> 二次解码产生乱码。
	decoded, err := parse.DecodeHTMLBytes(body, charsetFromContentType(ct))
	if err != nil {
		slog.Warn("html charset decode failed, using raw bytes", "url", rawURL, "error", err)
		decoded = body
	}

	// 后两步：头与 <meta> 都没声明时 DecodeHTMLBytes 会原样返回。此时若字节不是
	// 合法 UTF-8，按 GBK 兜底——否则中文站点会以乱码进入渲染器。
	if !utf8.Valid(decoded) {
		gbk, gerr := simplifiedchinese.GBK.NewDecoder().Bytes(decoded)
		if gerr == nil && utf8.Valid(gbk) {
			slog.Warn("html bytes not valid utf-8, decoded as gbk", "url", rawURL)
			decoded = gbk
		} else {
			slog.Warn("html bytes not valid utf-8 and gbk fallback failed, using raw bytes", "url", rawURL)
		}
	}
	return decoded, nil
}

// isHTMLContentType 判断 Content-Type 是否为可解析的 HTML。
func isHTMLContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	}
	return mt == "text/html" || mt == "application/xhtml+xml"
}

// charsetFromContentType 提取 Content-Type 中的 charset 参数；无则返回空串。
func charsetFromContentType(ct string) string {
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return params["charset"]
}
