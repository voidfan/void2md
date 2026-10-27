package chunk

import (
	"regexp"
	"sort"
	"strings"
)

// ensureBlockBlankLines inserts blank lines between HTML blocks and adjacent Markdown.
// Matches Python chunker._ensure_block_blank_lines: in CommonMark, <table>/<details>/
// <summary> open an HTML block that runs until a blank line, so without one, following
// Markdown (tables, headings, bold, etc.) gets swallowed. Normalizes newline runs next
// to these tags and images to \n\n (idempotent). Applied in Chunk() after PostProcess,
// only to non-code chunks — ![](url) inside fenced code is literal and must not change.
//
// Optimized vs. the original 6 sequential ReplaceAllString passes: collects runs via
// FindAllStringSubmatchIndex and rebuilds once. Output is byte-identical because each
// regex's match sequence is the same as ReplaceAllString's (left-to-right, non-overlapping),
// passes only rewrite runs (never the flanking markers), and runs are maximal disjoint
// \n+ spans whose normalization is idempotent — so the union equals sequential replacement.
func ensureBlockBlankLines(text string) string {
	lowered := strings.ToLower(text)
	if !strings.Contains(lowered, "<table") &&
		!strings.Contains(lowered, "</table") &&
		!strings.Contains(lowered, "<details") &&
		!strings.Contains(lowered, "</details") &&
		!strings.Contains(lowered, "<summary") &&
		!strings.Contains(lowered, "</summary") &&
		!strings.Contains(text, "![") {
		return text
	}

	runs := collectBlockBlankRuns(text, lowered)
	if len(runs) == 0 {
		return text
	}

	var sb strings.Builder
	sb.Grow(len(text))
	prev := 0
	for _, r := range runs {
		sb.WriteString(text[prev:r[0]])
		sb.WriteString("\n\n")
		prev = r[1]
	}
	sb.WriteString(text[prev:])
	return sb.String()
}

// collectBlockBlankRuns collects newline-run spans (original-text coordinates) to normalize.
// Each regex runs only when its marker is present; results are sorted by start and deduped.
func collectBlockBlankRuns(text, lowered string) [][2]int {
	var runs [][2]int
	appendRuns := func(re *regexp.Regexp) {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			// Group 2 is the run.
			runs = append(runs, [2]int{m[4], m[5]})
		}
	}
	if strings.Contains(lowered, "</table") {
		appendRuns(htmlTableAfter)
	}
	if strings.Contains(lowered, "<table") {
		appendRuns(htmlTableBefore)
	}
	if strings.Contains(text, "![") {
		appendRuns(htmlImgAfter)
		appendRuns(htmlImgBefore)
	}
	if strings.Contains(lowered, "<details") ||
		strings.Contains(lowered, "</details") ||
		strings.Contains(lowered, "<summary") ||
		strings.Contains(lowered, "</summary") {
		appendRuns(htmlDetailsAfter)
		appendRuns(htmlDetailsBefore)
	}
	if len(runs) == 0 {
		return runs
	}
	// Per-regex matches are individually sorted; sort across blocks. Maximal runs are
	// disjoint, so equal starts mean the same run — dedupe before rebuilding in order.
	sort.Slice(runs, func(i, j int) bool { return runs[i][0] < runs[j][0] })
	out := runs[:0]
	for i, r := range runs {
		if i > 0 && out[len(out)-1][0] == r[0] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ── Regexes (match Python _HTML_*_AFTER/_BEFORE; IMG needs no IGNORECASE, ! is literal) ──

// htmlTableAfter matches </table> immediately followed by a non-space char (no blank line).
var htmlTableAfter = regexp.MustCompile(`(?i)(</table\s*>)(\n+)(\S)`)

// htmlTableBefore matches a non-space char immediately followed by <table> (no blank line).
var htmlTableBefore = regexp.MustCompile(`(?i)(\S)(\n+)(<table\b)`)

// htmlImgAfter matches a Markdown image immediately followed by a non-space char.
var htmlImgAfter = regexp.MustCompile(`(!\[[^\]]*\]\([^)]+\))(\n+)(\S)`)

// htmlImgBefore matches a non-space char immediately followed by a Markdown image.
var htmlImgBefore = regexp.MustCompile(`(\S)(\n+)(!\[[^\]]*\]\()`)

// htmlDetailsAfter matches </?details|summary> immediately followed by a non-space char (HTML block boundary).
var htmlDetailsAfter = regexp.MustCompile(`(?i)(</?(?:details|summary)\b[^>]*>)(\n+)(\S)`)

// htmlDetailsBefore matches a non-space char immediately followed by </?details|summary>.
var htmlDetailsBefore = regexp.MustCompile(`(?i)(\S)(\n+)(</?(?:details|summary)\b[^>]*>)`)
