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

	"void2md/internal/parse"
)

// Crawl 从 opts.URL 出发做同域广度优先抓取。
//
// 终态判定（设计 §2.2，唯一入口）：
//   - 抓到 >=1 页 → 返回 Result（是否提前停止由 Result.Truncated / StopReason 表示）
//   - 抓到 0 页   → 返回 error，原因具体可读（入口页失败 / selector 无匹配 / 入口内容为空）
//
// onProgress 可选，每次接收一页后以当前已完成页数回调，作为 running 状态的进度信号。
func Crawl(ctx context.Context, opts Options, onProgress func(done int)) (*Result, error) {
	entry, err := NormalizeURL(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid entry url: %w", err)
	}

	// 参数兜底
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

	// 这些变量只在调度 goroutine 里读写：每批 worker 只往各自的 results[i] 写，
	// wg.Wait() 之后才由调度 goroutine 汇总。因此**不需要 mutex**——
	// 别照搬设计 §4.3 那句「visited 为 mutex 保护的 set」在这里加锁。
	var (
		pages      []Page
		totalBytes int64
		stop       StopReason
		entryErr   error
	)

	// process 处理单页：抓取 → 解析 DOM → 提取链接（全文档）→ 裁剪正文 → 渲染 Markdown。
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

		// 链接从**整个文档**提取（设计 §4.2 第 10 步），否则导航栏分页链接发现不了
		links := ExtractLinks(doc, base)

		scoped, err := parse.SelectNode(doc, opts.Selector)
		if err != nil {
			return Page{}, links, err
		}
		// 空页规则（设计 §4.2 第 7 步）：统一覆盖「selector 无匹配」「匹配到但无文本」
		// 「body 为空」三种情形，无需分别判定。
		if len(scoped) == 0 {
			return Page{}, links, fmt.Errorf("empty content (selector %q matched nothing)", opts.Selector)
		}

		// 字节已在 Fetch 阶段解码为 UTF-8，此处必须以 "utf-8" 传入，
		// 否则解析器会按 <meta charset> 二次解码产生乱码。
		md, _, err := parse.ParseHTMLBytesToMarkdownWithCharset(taskCtx, scoped, rawURL, "utf-8")
		if err != nil {
			return Page{}, links, fmt.Errorf("markdown render failed: %w", err)
		}
		if strings.TrimSpace(md) == "" {
			return Page{}, links, fmt.Errorf("empty content (selector %q produced no text)", opts.Selector)
		}
		return Page{URL: rawURL, Title: extractTitle(doc, rawURL), Markdown: md}, links, nil
	}

	// visited 必须跨层保持：每层重建会让已抓页面被重复入队
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
			stop = StopMaxPage // frontier 还有剩余，说明被上限截断
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
			// 链接入队必须在 err 判定**之前**（设计 §4.2 第 10 步）：process 在
			// 「selector 未命中」「正文为空」这些**页面被判空丢弃**的路径上照样返回
			// 它已抽出的链接（见上面那几处 `return Page{}, links, err`）。放在
			// `continue` 之后，这些链接会被整批丢掉——而导航页/聚合页恰恰最容易是
			// 这种形状，代价是整棵子树抓不到，任务却报 success。
			// （取回失败的页面 links 为 nil，这里是空转，不影响「子页失败只记日志」。）
			for _, l := range r.links {
				// 同域过滤（设计 §4.2 第 11 步）。漏掉这一步会让爬虫跟随所有外链。
				if ShouldEnqueue(l, entry, visited) {
					visited[l] = true
					next = append(next, l)
				}
			}
			if r.err != nil {
				// 子页失败只记日志（设计 §5）；入口页失败记为任务级原因
				slog.Warn("html crawl page skipped", "url", r.url, "reason", r.err.Error())
				if r.url == entry.String() {
					entryErr = r.err
				}
				continue
			}
			// opts.MaxTaskBytes <= 0 在这里表示**不限**（与上面 MaxPage/Concurrency/
			// TaskTimeout 的 `<=0 → 默认值` 口径不同，是有意的）：
			// 本包是内部库，直接调用方用零值 Options 时应得到「能跑」的行为，而不是
			// 「第一页就撞上限」。生产路径上它恒为正数——service 层用
			// `config.HTMLCrawlConfig.Normalize()` 保证配置里的 0 会先被补成默认值
			// （PDF2TEXT_HTML_MAX_TASK_BYTES_MB，默认 200MB），见收尾复审 F6。
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

	// 终态判定（设计 §2.2）：0 页 → error，原因具体可读
	//
	// 判断顺序有意让 stop 先于 entryErr：任务超时时入口页的 err 必然是 taskCtx
	// 取消产生的 "context deadline exceeded"，若先看 entryErr，「任务超时」这个
	// 更具体、更可操作的原因会被盖成一句泛化的请求失败，设计 §2.2 的
	// 「超时且 0 页时 error 必须说明是超时」就落不了地。
	// 入口页自身的失败原因仍拼在末尾，便于排查（如超时同时入口页 404）。
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
		// 字节预算在第一页就挡下时 pages 保持 0（命中预算是在 append **之前** break），
		// 若不给这条成因单独一支，就会落到最后那句泛化的 "produced no content"——
		// 运维会去查站点，而真正的原因是 PDF2TEXT_HTML_MAX_TASK_BYTES_MB 配小了。
		// 放在 entryErr 之后：入口页**自身**失败时，那条原因更具体、更该优先暴露。
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

// extractTitle 三级回退取标题：<title> → 首个 <h1> → URL 原文（对齐 FastGPT）。
func extractTitle(doc *html.Node, fallback string) string {
	if t := strings.TrimSpace(findText(doc, "title")); t != "" {
		return t
	}
	if h := strings.TrimSpace(findText(doc, "h1")); h != "" {
		return h
	}
	return fallback
}

// findText 返回子树中首个指定标签的纯文本内容。
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

// textContent 递归拼接节点的全部文本。
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
