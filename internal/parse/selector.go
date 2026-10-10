package parse

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// SelectHTML 按 CSS 选择器从 HTML 字节中抽取**第一个**匹配元素，序列化回 HTML 字节。
// selector 为空或纯空白时按 "body" 处理；未匹配到任何元素时返回长度为 0 的字节切片与 nil error
// （由调用方按「空页规则」决定跳过，见设计 §4.2 第 7 步）。
func SelectHTML(htmlBytes []byte, selector string) ([]byte, error) {
	doc, err := html.Parse(bytes.NewReader(htmlBytes))
	if err != nil {
		return nil, fmt.Errorf("html parse error: %w", err)
	}
	return SelectNode(doc, selector)
}

// SelectNode 与 SelectHTML 相同，但作用于已解析的节点（不限于文档根，
// 传入任一棵子树的根同样合法），供调用方复用一个 DOM
// （internal/crawler 需要同时做链接提取与正文裁剪）。
//
// 语义与 cascadia.Selector.MatchFirst 一致：**选择器也匹配传入节点自身**，
// 而不只是搜索它的子孙节点（cascadia.Query 才是「排除自身、只搜子孙」）。
// 于是传入子树根时，子树根自己命中选择器必须返回它自己；若改用 cascadia.Query，
// 这种情况会返回长度为 0 的字节切片，上层会按「selector matched nothing」丢掉整页。
//
// 注意：当前唯一的调用点（internal/crawler）传的是**文档根**，而文档节点不会被
// 任何 CSS 选择器命中，所以两种 API 在那里行为相同、今天看不出差别。这条自含语义
// 是为「传子树根」这个受支持的用法钉住的——**不要因为它今天不被触发就换成
// cascadia.Query**，那会让将来复用同一个 DOM 做多级裁剪的调用方静默丢页。
func SelectNode(doc *html.Node, selector string) ([]byte, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		selector = "body"
	}
	sel, err := cascadia.Compile(selector)
	if err != nil {
		return nil, fmt.Errorf("invalid selector %q: %w", selector, err)
	}
	node := sel.MatchFirst(doc)
	if node == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, node); err != nil {
		return nil, fmt.Errorf("html render error: %w", err)
	}
	return buf.Bytes(), nil
}
