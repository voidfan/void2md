package chunk

import (
	"regexp"
	"strings"
)

// PackByBudget greedily packs units into groups where Sum(TLen) + fixedOverhead <= budget.
func PackByBudget(units []string, budget int, fixedOverhead int) [][]string {
	if len(units) == 0 {
		return nil
	}
	// Batch token count: avoids one cgo call per unit (tables can have tens of
	// thousands of rows). Counts correspond 1:1 to units; packing logic unchanged.
	toks := TLenBatch(units)
	var groups [][]string
	var cur []string
	curTok := 0
	room := budget - fixedOverhead
	if room < 1 {
		room = 1
	}
	for i, u := range units {
		ut := toks[i]
		if len(cur) > 0 && curTok+ut > room {
			groups = append(groups, cur)
			cur = nil
			curTok = 0
		}
		cur = append(cur, u)
		curTok += ut
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

// SplitText recursively splits text by multi-level separators; each part has TLen <= budget.
func SplitText(text string, budget int) []string {
	return splitTextRecursive(text, budget, 0)
}

func splitTextRecursive(text string, budget int, sepIdx int) []string {
	if TLen(text) <= budget {
		return []string{text}
	}

	if sepIdx >= len(RECURSIVE_SEPARATORS) {
		return HardTokenSplit(text, budget)
	}

	sep := RECURSIVE_SEPARATORS[sepIdx]
	if sep == "" {
		return HardTokenSplit(text, budget)
	}

	parts := strings.Split(text, sep)
	var chunks []string
	current := ""

	for _, part := range parts {
		candidate := current
		if current != "" {
			candidate = current + sep + part
		} else {
			candidate = part
		}

		if TLen(candidate) <= budget {
			current = candidate
			continue
		}

		if current != "" {
			if TLen(current) > budget {
				chunks = append(chunks, splitTextRecursive(current, budget, sepIdx+1)...)
			} else {
				chunks = append(chunks, current+sep)
			}
			current = ""
		}

		if TLen(part) > budget {
			sub := splitTextRecursive(part, budget, sepIdx+1)
			if len(sub) > 0 {
				chunks = append(chunks, sub[:len(sub)-1]...)
				current = sub[len(sub)-1]
			}
		} else {
			current = part
		}
	}

	if current != "" {
		if TLen(current) > budget {
			chunks = append(chunks, splitTextRecursive(current, budget, sepIdx+1)...)
		} else {
			chunks = append(chunks, current)
		}
	}
	return chunks
}

// SplitTable is the table-splitting entry point: detects HTML/Markdown and splits
// by budget while preserving table structure.
func SplitTable(text string, budget int) []string {
	if TLen(text) <= budget {
		return []string{text}
	}

	if HTML_TABLE_OPEN.MatchString(text) {
		openTag, header, body, pre, post := ParseHTMLTable(text)
		isHTML := true
		ok := openTag != "" && len(body) > 0
		return splitTableImpl(text, budget, isHTML, ok, openTag, header, body, pre, post)
	}

	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > 0 && MD_TABLE_ROW.MatchString(lines[0]) {
		header, body := ParseMDTable(text)
		isHTML := false
		ok := header != "" && len(body) > 0
		return splitTableImpl(text, budget, isHTML, ok, "", header, body, "", "")
	}

	return SplitText(text, budget)
}

func splitTableImpl(text string, budget int, isHTML bool, ok bool,
	openTag, header string, body []string, pre, post string) []string {

	if !ok {
		return SplitText(text, budget)
	}

	closeTag := "</table>"
	overhead := TLen(header)
	if isHTML {
		overhead = TLen(openTag) + TLen(header) + TLen(closeTag)
		if pre != "" {
			overhead += TLen(pre) + 1
		}
		if post != "" {
			overhead += TLen(post) + 1
		}
	}

	if overhead >= budget {
		return SplitText(text, budget)
	}

	var out []string
	var postExtra []string

	if isHTML {
		if pre != "" && TLen(pre) > budget {
			out = append(out, SplitText(pre, budget)...)
			pre = ""
		}
		if post != "" && TLen(post) > budget {
			postExtra = SplitText(post, budget)
			post = ""
		}
	}

	groups := PackByBudget(body, budget, overhead)

	if len(groups) <= 1 && TLen(text) > budget {
		bodyText := strings.Join(body, "\n")
		bodyBudget := budget - overhead
		if !isHTML {
			bodyBudget--
		}
		bodyParts := SplitText(bodyText, bodyBudget)
		if len(bodyParts) <= 1 {
			return []string{text}
		}
		for _, p := range bodyParts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			out = append(out, assembleChunk(isHTML, p, openTag, header, closeTag, pre, post, nil))
		}
		out = append(out, postExtra...)
		return out
	}

	for _, group := range groups {
		bodyText := ""
		if isHTML {
			bodyText = strings.Join(group, "\n")
		}
		out = append(out, assembleChunk(isHTML, bodyText, openTag, header, closeTag, pre, post, group))
	}
	out = append(out, postExtra...)
	return out
}

func assembleChunk(isHTML bool, bodyText, openTag, header, closeTag, pre, post string, mdGroup []string) string {
	if isHTML {
		var seg []string
		if pre != "" {
			seg = append(seg, pre)
		}
		seg = append(seg, openTag, header, bodyText, closeTag)
		if post != "" {
			seg = append(seg, post)
		}
		return strings.Join(seg, "\n")
	}
	if mdGroup != nil {
		return strings.Join(append([]string{header}, mdGroup...), "\n")
	}
	return strings.Join([]string{header, bodyText}, "\n")
}

// extractTopLevelTRs extracts direct child <tr>s of the outermost <table>, skipping
// rows of nested tables.
//
// Semantics are identical to HTML_TABLE_TR_TAG.FindAllStringSubmatchIndex: matches
// <(/)?(table|tr)\b[^>]*> case-insensitively, left-to-right non-overlapping (resumes
// after each tag, skipping <table>/<tr> inside attribute values). Implemented as a
// single manual byte scan to avoid FindAll's per-match []int and per-tag ToLower
// allocations.
func extractTopLevelTRs(html string) []string {
	var rows []string
	td, trd := 0, 0
	st := -1
	for i := 0; i < len(html); i++ {
		if html[i] != '<' {
			continue
		}
		j := i + 1
		close := false
		if j < len(html) && html[j] == '/' {
			close = true
			j++
		}
		nameLen, isTable := matchTableOrTr(html, j)
		if nameLen == 0 {
			continue
		}
		// \b boundary: tag name must be followed by a non-word byte (or end of string)
		if j+nameLen < len(html) && isWordByte(html[j+nameLen]) {
			continue
		}
		// [^>]*> advances to the first > (end of match)
		k := j + nameLen
		for k < len(html) && html[k] != '>' {
			k++
		}
		if k == len(html) {
			continue // no '>', not a tag
		}
		matchEnd := k + 1
		if isTable {
			if close {
				td--
			} else {
				td++
			}
		} else {
			if close {
				trd--
				if trd < 0 {
					trd = 0
				}
				if td == 1 && trd == 0 && st >= 0 {
					rows = append(rows, html[st:matchEnd])
				}
			} else {
				if td == 1 && trd == 0 {
					st = i
				}
				trd++
			}
		}
		i = matchEnd - 1 // resume at match end (non-overlapping)
	}
	return rows
}

// matchTableOrTr matches (table|tr) at html[j:], case-insensitively.
// Returns the tag name length (5 or 2) and whether it is table; (0, false) if no match.
func matchTableOrTr(html string, j int) (nameLen int, isTable bool) {
	if j+5 <= len(html) && asciiEqualFold(html[j:j+5], "table") {
		return 5, true
	}
	if j+2 <= len(html) && asciiEqualFold(html[j:j+2], "tr") {
		return 2, false
	}
	return 0, false
}

// asciiEqualFold compares s with ASCII literal lit case-insensitively (non-ASCII bytes
// compare raw, matching Go regexp (?i) behavior for ASCII).
func asciiEqualFold(s, lit string) bool {
	if len(s) != len(lit) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		l := lit[i]
		if 'A' <= l && l <= 'Z' {
			l += 'a' - 'A'
		}
		if c != l {
			return false
		}
	}
	return true
}

// isWordByte reports whether the byte is in Go regexp's \w set ([0-9A-Za-z_]).
func isWordByte(c byte) bool {
	return '0' <= c && c <= '9' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || c == '_'
}

// ParseHTMLTable parses an HTML table; returns (openTag, header, bodyRows, pre, post).
func ParseHTMLTable(text string) (openTag, header string, bodyRows []string, pre, post string) {
	openM := HTML_TABLE_OPEN.FindStringIndex(text)
	if openM == nil {
		return "", "", nil, strings.TrimSpace(text), ""
	}
	openTag = text[openM[0]:openM[1]]
	pre = strings.TrimSpace(text[:openM[0]])

	// depth-count to find the matching </table> (avoids matching an inner tag when nested)
	depth := 1
	pos := openM[1]
	closeEnd := len(text)
	for depth > 0 && pos < len(text) {
		nxtOpen := HTML_TABLE_OPEN.FindStringIndex(text[pos:])
		nxtClose := HTML_TABLE_CLOSE.FindStringIndex(text[pos:])
		if nxtClose == nil {
			break
		}
		if nxtOpen != nil && nxtOpen[0] < nxtClose[0] {
			depth++
			pos += nxtOpen[1]
		} else {
			depth--
			if depth == 0 {
				closeEnd = pos + nxtClose[1]
				break
			}
			pos += nxtClose[1]
		}
	}
	if depth != 0 {
		return openTag, "", nil, pre, ""
	}

	tableHTML := text[openM[0]:closeEnd]
	post = strings.TrimSpace(text[closeEnd:])

	rows := extractTopLevelTRs(tableHTML)
	if len(rows) == 0 {
		return tableHTML, "", nil, pre, post
	}

	// leading consecutive <tr>s containing only <th> (no <td>) count as header
	h := 0
	for _, row := range rows {
		if !HTML_TD_PRESENT.MatchString(row) {
			h++
		} else {
			break
		}
	}
	if h == 0 {
		h = 1
	}

	// multi-row header: if the last header row has colspan, the next row may hold sub-column names
	hasColspan := regexp.MustCompile(`(?i)colspan\s*=\s*["']?\d`)
	for h < len(rows) && hasColspan.MatchString(rows[h-1]) {
		prevCells := len(regexp.MustCompile(`(?i)<(td|th)\b`).FindAllString(rows[h-1], -1))
		nextCells := len(regexp.MustCompile(`(?i)<(td|th)\b`).FindAllString(rows[h], -1))
		if nextCells < prevCells {
			h++
		} else {
			break
		}
	}

	header = strings.Join(rows[:h], "")
	return openTag, header, rows[h:], pre, post
}

// ParseMDTable extracts (header+divider, bodyRows) from Markdown table content.
func ParseMDTable(text string) (header string, bodyRows []string) {
	re := regexp.MustCompile(`(\|[^\n]*\|)\s*\n(\|(?:[\s\-:]*\|)+)\s*(?:\n|$)`)
	m := re.FindStringSubmatch(text)
	if m != nil {
		header = strings.TrimSpace(m[1] + "\n" + m[2])
		body := strings.TrimSpace(text[len(header):])
		for _, r := range strings.Split(body, "\n") {
			r = strings.TrimSpace(r)
			if r != "" {
				bodyRows = append(bodyRows, r)
			}
		}
	} else {
		header = ""
		for _, r := range strings.Split(text, "\n") {
			r = strings.TrimSpace(r)
			if r != "" {
				bodyRows = append(bodyRows, r)
			}
		}
	}
	return header, bodyRows
}
