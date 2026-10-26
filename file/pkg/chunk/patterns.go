package chunk

import "regexp"

// DEFAULT_SEPARATOR 元素内部描述与主体的分隔符。
var DEFAULT_SEPARATOR = "\n\n\n\n\n"

// RECURSIVE_SEPARATORS 多级递归切分分隔符（从粗到细）。
var RECURSIVE_SEPARATORS = []string{"\n\n", "\n", "。", "；", ". ", "; ", " "}

// HEADING_PATTERN 匹配 Markdown/Setext 标题：
//   - ATX: 1-6 个 # 后跟空格 + 标题文本
//   - Setext: === 或 --- 下划线（识别前一行）
// 组1：级别标记（### 或空用于 setext）、组2：标题文本
var HEADING_PATTERN = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

// CODE_FENCE_PATTERN 匹配围栏代码块起始：``` 或 ~~~（≥3个）。
// 组 fence：围栏符号，组 lang：语言标识
var CODE_FENCE_PATTERN = regexp.MustCompile(`^(?P<fence>` + "`" + `{3,}|~{3,})\s*(?P<lang>[^\s` + "`" + `~]*)\s*$`)

// HTML_TABLE_OPEN 匹配 <table ...> 开标签。
var HTML_TABLE_OPEN = regexp.MustCompile(`(?i)<table\b[^>]*>`)

// HTML_TABLE_CLOSE 匹配 </table> 闭标签。
var HTML_TABLE_CLOSE = regexp.MustCompile(`(?i)</table\s*>`)

// HTML_TR 匹配 <tr>...</tr> 行。
var HTML_TR = regexp.MustCompile(`(?is)<tr\b[^>]*>.*?</tr\s*>`)

// HTML_TABLE_TR_TAG 匹配 <table>/</table>/<tr>/</tr> 标签（大小写不敏感）。
// Go regexp 不支持反向引用，因此用捕获组 (/)? 表示可选斜杠，
// 使用 FindAllStringSubmatchIndex 时：组1（斜杠）在 m[2]:m[3]，存在 iff m[2] >= 0；
// 组2（table/tr）在 m[4]:m[5]。
var HTML_TABLE_TR_TAG = regexp.MustCompile(`(?is)<(/)?(table|tr)\b[^>]*>`)

// HTML_TD_PRESENT 匹配 <td 或 <th 标签存在。
var HTML_TD_PRESENT = regexp.MustCompile(`(?i)<t[dh]\b`)

// MD_TABLE_ROW 匹配 Markdown 管道表格行：| col1 | col2 |
var MD_TABLE_ROW = regexp.MustCompile(`^\|.*\|\s*$`)

// MD_TABLE_DIVIDER 匹配 Markdown 表格分隔行：|---|:---:|
var MD_TABLE_DIVIDER = regexp.MustCompile(`^\|(?:\s*:?-+:?\s*\|)+\s*$`)

// IMAGE_PATTERN 匹配图片 markdown：![alt](url)
var IMAGE_PATTERN = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

// TABLE_CAPTION 匹配表格标题："表1 xxx" 或 "Table 1 xxx"
var TABLE_CAPTION = regexp.MustCompile(`(?i)^(表|Table|Tab)\s*\d+[\.\-\s:]*.*`)

// FIGURE_CAPTION 匹配图片标题："图1 xxx" 或 "Figure 1 xxx"
var FIGURE_CAPTION = regexp.MustCompile(`(?i)^(图|Figure|Fig)\s*\.?\s*\d+[\.\-\s:]*.*`)

// ABSTRACT_PATTERN 匹配摘要/abstract 关键词。
var ABSTRACT_PATTERN = regexp.MustCompile(`(?i)^\s*(abstract|摘要)\s*$`)

// SECTION_HEADING 识别章节号（用于跨章节判定）。
var SECTION_HEADING = regexp.MustCompile(`^(?:#{1,6}\s|\d+\.\d+(?:\.\d+)?\s|第[一二三四五六七八九十\d]+[章节])`)

// HTML_COMMENT 匹配 HTML 注释 <!-- ... -->
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// preCodeOpenPattern 匹配 <pre>/<code> 开标签（大小写不敏感）。
// 原 parseLines 内联 regexp.MatchString 每次调用都会重新编译，提升为包级。
var preCodeOpenPattern = regexp.MustCompile(`(?i)<(pre|code)\b[^>]*>`)

// preCodeClosePattern 匹配 </pre>/</code> 闭标签（大小写不敏感）。
var preCodeClosePattern = regexp.MustCompile(`(?i)</(pre|code)\s*>`)

// paraNonSpaceImageChars 匹配去图后仍存在的非空白、非图片语法字符（判纯图片段落）。
var paraNonSpaceImageChars = regexp.MustCompile(`[^\s\[\]!\(\)]`)

// HTML_PRE_CODE_STRIP 匹配 <pre> 或 <code> 块。
var htmlPreCodeStrip = regexp.MustCompile(`(?is)<pre\b[^>]*>.*?</pre\s*>|<code\b[^>]*>.*?</code\s*>`)

// Setext 风格标题检测：非空行之后紧跟全是 === 或 --- 的行
var setextLine = regexp.MustCompile(`^(={3,}|-{3,})\s*$`)

// stripHTMLNoise 剥离 HTML 注释和 <pre>/<code> 块，便于后续判定结构。
func stripHTMLNoise(text string) string {
	text = htmlComment.ReplaceAllString(text, "")
	text = htmlPreCodeStrip.ReplaceAllString(text, "")
	return text
}