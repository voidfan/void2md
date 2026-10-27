package chunk

import "regexp"

// DEFAULT_SEPARATOR separates an element's description from its body.
var DEFAULT_SEPARATOR = "\n\n\n\n\n"

// RECURSIVE_SEPARATORS are multi-level recursive split separators (coarse to fine).
var RECURSIVE_SEPARATORS = []string{"\n\n", "\n", "。", "；", ". ", "; ", " "}

// HEADING_PATTERN matches Markdown headings (ATX: 1-6 # + space + text).
// Group 1: level marker; group 2: heading text.
var HEADING_PATTERN = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

// CODE_FENCE_PATTERN matches a fenced code block start: ``` or ~~~ (≥3).
// Group fence: fence chars; group lang: language tag.
var CODE_FENCE_PATTERN = regexp.MustCompile(`^(?P<fence>` + "`" + `{3,}|~{3,})\s*(?P<lang>[^\s` + "`" + `~]*)\s*$`)

// HTML_TABLE_OPEN matches a <table ...> open tag.
var HTML_TABLE_OPEN = regexp.MustCompile(`(?i)<table\b[^>]*>`)

// HTML_TABLE_CLOSE matches a </table> close tag.
var HTML_TABLE_CLOSE = regexp.MustCompile(`(?i)</table\s*>`)

// HTML_TR matches a <tr>...</tr> row.
var HTML_TR = regexp.MustCompile(`(?is)<tr\b[^>]*>.*?</tr\s*>`)

// HTML_TABLE_TR_TAG matches <table>/</table>/<tr>/</tr> tags (case-insensitive).
// Go regexp lacks backreferences, so (/)? captures the optional slash:
// in FindAllStringSubmatchIndex, group 1 (slash) is m[2]:m[3], present iff m[2] >= 0;
// group 2 (table/tr) is m[4]:m[5].
var HTML_TABLE_TR_TAG = regexp.MustCompile(`(?is)<(/)?(table|tr)\b[^>]*>`)

// HTML_TD_PRESENT matches a <td or <th tag.
var HTML_TD_PRESENT = regexp.MustCompile(`(?i)<t[dh]\b`)

// MD_TABLE_ROW matches a Markdown pipe table row: | col1 | col2 |
var MD_TABLE_ROW = regexp.MustCompile(`^\|.*\|\s*$`)

// MD_TABLE_DIVIDER matches a Markdown table divider row: |---|:---:|
var MD_TABLE_DIVIDER = regexp.MustCompile(`^\|(?:\s*:?-+:?\s*\|)+\s*$`)

// IMAGE_PATTERN matches image markdown: ![alt](url)
var IMAGE_PATTERN = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

// TABLE_CAPTION matches table captions like "表1 xxx" or "Table 1 xxx".
var TABLE_CAPTION = regexp.MustCompile(`(?i)^(表|Table|Tab)\s*\d+[\.\-\s:]*.*`)

// FIGURE_CAPTION matches figure captions like "图1 xxx" or "Figure 1 xxx".
var FIGURE_CAPTION = regexp.MustCompile(`(?i)^(图|Figure|Fig)\s*\.?\s*\d+[\.\-\s:]*.*`)

// ABSTRACT_PATTERN matches the abstract heading keyword (English or Chinese).
var ABSTRACT_PATTERN = regexp.MustCompile(`(?i)^\s*(abstract|摘要)\s*$`)

// SECTION_HEADING detects section numbers (for cross-section checks), incl. Chinese 第X章/节.
var SECTION_HEADING = regexp.MustCompile(`^(?:#{1,6}\s|\d+\.\d+(?:\.\d+)?\s|第[一二三四五六七八九十\d]+[章节])`)

// htmlComment matches HTML comments <!-- ... -->
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// preCodeOpenPattern matches <pre>/<code> open tags (case-insensitive).
// Hoisted to package level: parseLines previously recompiled it on every call.
var preCodeOpenPattern = regexp.MustCompile(`(?i)<(pre|code)\b[^>]*>`)

// preCodeClosePattern matches </pre>/</code> close tags (case-insensitive).
var preCodeClosePattern = regexp.MustCompile(`(?i)</(pre|code)\s*>`)

// paraNonSpaceImageChars matches non-space, non-image-syntax chars left after removing
// images (pure-image paragraph detection).
var paraNonSpaceImageChars = regexp.MustCompile(`[^\s\[\]!\(\)]`)

// htmlPreCodeStrip matches <pre> or <code> blocks.
var htmlPreCodeStrip = regexp.MustCompile(`(?is)<pre\b[^>]*>.*?</pre\s*>|<code\b[^>]*>.*?</code\s*>`)

// setextLine detects Setext headings: a line of only === or --- after a non-empty line.
var setextLine = regexp.MustCompile(`^(={3,}|-{3,})\s*$`)

// stripHTMLNoise removes HTML comments and <pre>/<code> blocks before structure checks.
func stripHTMLNoise(text string) string {
	text = htmlComment.ReplaceAllString(text, "")
	text = htmlPreCodeStrip.ReplaceAllString(text, "")
	return text
}
