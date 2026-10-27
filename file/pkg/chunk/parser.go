package chunk

import (
	"regexp"
	"strings"
)

// Parse runs the full parse: line scan → parent links → caption merge.
func Parse(text string, separator string) []Element {
	elements := parseLines(text)
	attachParents(elements)
	return mergeCaptions(elements, separator)
}

// -- Phases 1-5: line scan --

func parseLines(text string) []Element {
	lines := strings.Split(text, "\n")
	var elements []Element
	eid := 0
	inPreCode := false
	i := 0

	for i < len(lines) {
		line := lines[i]
		stripped := strings.TrimSpace(line)

		if stripped == "" {
			i++
			continue
		}

		// track bare <pre>/<code> tags
		if preCodeOpenPattern.MatchString(stripped) {
			if !preCodeClosePattern.MatchString(stripped) {
				inPreCode = true
			}
		}
		if preCodeClosePattern.MatchString(stripped) {
			inPreCode = false
		}

		// 1. Fenced code block
		if m := CODE_FENCE_PATTERN.FindStringSubmatch(stripped); m != nil {
			fence := m[1]
			lang := ""
			if len(m) > 2 {
				lang = m[2]
			}
			start := i
			collected := []string{line}
			i++
			closed := false
			for i < len(lines) {
				collected = append(collected, lines[i])
				cs := strings.TrimSpace(lines[i])
				if strings.HasPrefix(cs, fence[0:1]) && strings.TrimRight(cs, fence[0:1]) == "" && len(cs) >= len(fence) {
					i++
					closed = true
					break
				}
				i++
			}
			eid++
			elements = append(elements, Element{
				ID:        eid,
				Type:      "code",
				Text:      strings.Join(collected, "\n"),
				Metadata:  map[string]any{"lang": lang, "closed": closed},
				StartLine: start + 1,
				EndLine:   i,
			})
			continue
		}

		// 2. HTML table (may span lines)
		commentFree := stripHTMLNoise(stripped)
		if !inPreCode && HTML_TABLE_OPEN.MatchString(commentFree) {
			start := i
			collected := []string{line}
			closedInLine := HTML_TABLE_CLOSE.MatchString(stripped)
			i++
			if !closedInLine {
				for i < len(lines) {
					collected = append(collected, lines[i])
					if HTML_TABLE_CLOSE.MatchString(lines[i]) {
						i++
						break
					}
					i++
				}
			}
			eid++
			elements = append(elements, Element{
				ID:        eid,
				Type:      "table",
				Text:      strings.Join(collected, "\n"),
				Metadata:  map[string]any{"format": "html"},
				StartLine: start + 1,
				EndLine:   i,
			})
			continue
		}

		// 3. Heading detection
		if m := HEADING_PATTERN.FindStringSubmatch(stripped); m != nil {
			level := len(m[1])
			titleText := strings.TrimSpace(m[2])
			eid++
			elements = append(elements, Element{
				ID:        eid,
				Type:      "title",
				Text:      strings.Repeat("#", level) + " " + titleText,
				Metadata:  map[string]any{"level": level},
				StartLine: i + 1,
				EndLine:   i + 1,
			})
			i++
			continue
		}

		// Setext-style heading
		if i+1 < len(lines) && setextLine.MatchString(strings.TrimSpace(lines[i+1])) {
			nextLine := strings.TrimSpace(lines[i+1])
			level := 1
			if strings.HasPrefix(nextLine, "-") {
				level = 2
			}
			eid++
			elements = append(elements, Element{
				ID:        eid,
				Type:      "title",
				Text:      strings.Repeat("#", level) + " " + stripped,
				Metadata:  map[string]any{"level": level},
				StartLine: i + 1,
				EndLine:   i + 2,
			})
			i += 2
			continue
		}

		// 4. Markdown table (pipe row + divider row next)
		if MD_TABLE_ROW.MatchString(stripped) && i+1 < len(lines) && MD_TABLE_DIVIDER.MatchString(strings.TrimSpace(lines[i+1])) {
			start := i
			var rows []string
			for i < len(lines) && MD_TABLE_ROW.MatchString(strings.TrimSpace(lines[i])) {
				rows = append(rows, lines[i])
				i++
			}
			eid++
			elements = append(elements, Element{
				ID:        eid,
				Type:      "table",
				Text:      strings.Join(rows, "\n"),
				Metadata:  map[string]any{},
				StartLine: start + 1,
				EndLine:   i,
			})
			continue
		}

		// 5. Paragraph (Figure / Text)
		start := i
		var para []string
		for i < len(lines) {
			ln := lines[i]
			s := strings.TrimSpace(ln)
			if s == "" {
				break
			}
			if HEADING_PATTERN.MatchString(s) {
				break
			}
			if MD_TABLE_ROW.MatchString(s) && i+1 < len(lines) && MD_TABLE_DIVIDER.MatchString(strings.TrimSpace(lines[i+1])) {
				break
			}
			if CODE_FENCE_PATTERN.MatchString(s) {
				break
			}
			if !inPreCode && HTML_TABLE_OPEN.MatchString(stripHTMLNoise(s)) {
				break
			}
			para = append(para, ln)
			i++
		}

		paraText := strings.Join(para, "\n")
		eid++
		// pure-image paragraph → Figure (no non-space chars left after removing ![]())
		if IMAGE_PATTERN.MatchString(paraText) && !paraNonSpaceImageChars.MatchString(IMAGE_PATTERN.ReplaceAllString(paraText, "")) {
			elements = append(elements, Element{
				ID:        eid,
				Type:      "figure",
				Text:      paraText,
				Metadata:  map[string]any{},
				StartLine: start + 1,
				EndLine:   i,
			})
		} else {
			elements = append(elements, Element{
				ID:        eid,
				Type:      "text",
				Text:      paraText,
				Metadata:  map[string]any{},
				StartLine: start + 1,
				EndLine:   i,
			})
		}
	}

	return elements
}

// -- Phase: parent-child linking --

func attachParents(elements []Element) {
	type stackEntry struct {
		level int
		id    int
	}
	stack := []stackEntry{{level: -1, id: 0}}

	for i := range elements {
		el := &elements[i]
		if el.Type == "title" {
			raw := el.Metadata["level"]
			level, ok := raw.(int)
			if !ok {
				continue
			}
			for len(stack) > 0 && level <= stack[len(stack)-1].level {
				stack = stack[:len(stack)-1]
			}
			if len(stack) > 0 {
				parentLevel := stack[len(stack)-1].level
				parentID := stack[len(stack)-1].id
				el.Metadata["parent_id"] = parentID
				if parentID != 0 && level > parentLevel+1 && parentLevel > 0 {
					level = parentLevel + 1
					el.Metadata["level"] = level
				}
			}
			stack = append(stack, stackEntry{level: level, id: el.ID})
		} else {
			if len(stack) > 0 {
				el.Metadata["parent_id"] = stack[len(stack)-1].id
			}
		}
	}

	// non-title elements inherit level from their parent title
	byID := make(map[int]*Element)
	for i := range elements {
		byID[elements[i].ID] = &elements[i]
	}
	for i := range elements {
		el := &elements[i]
		if el.Type != "title" {
			pid, ok := el.Metadata["parent_id"].(int)
			if ok {
				if p, exists := byID[pid]; exists {
					if lvl, ok := p.Metadata["level"].(int); ok {
						el.Metadata["level"] = lvl
					}
				}
			}
		}
	}
}

// -- Phase: caption merge --

func mergeCaptions(elements []Element, separator string) []Element {
	out := make([]Element, len(elements))
	copy(out, elements)

	i := 0
	for i < len(out) {
		el := &out[i]
		var capRe *regexp.Regexp
		if el.Type == "figure" {
			capRe = FIGURE_CAPTION
		} else if el.Type == "table" {
			// tables: no caption merge, keep pure HTML table structure
			i++
			continue
		} else {
			i++
			continue
		}

		// caption merge applies to figures only
		if i > 0 && out[i-1].Type == "text" {
			prev := out[i-1]
			prevText := strings.TrimSpace(prev.Text)
			if capRe.MatchString(prevText) || !hasHeading(prevText) {
				el.Text = separator + prev.Text + "\n" + el.Text
				if prev.StartLine > 0 && (el.StartLine == 0 || prev.StartLine < el.StartLine) {
					el.StartLine = prev.StartLine
				}
				if prev.EndLine > el.EndLine {
					el.EndLine = prev.EndLine
				}
				out = append(out[:i-1], out[i:]...)
				i--
			}
		} else if i < len(out)-1 && out[i+1].Type == "text" {
			nxt := out[i+1]
			nxtText := strings.TrimSpace(nxt.Text)
			if capRe.MatchString(nxtText) || !hasHeading(nxtText) {
				el.Text = el.Text + "\n" + separator + nxt.Text
				if nxt.EndLine > el.EndLine {
					el.EndLine = nxt.EndLine
				}
				if nxt.StartLine > 0 && (el.StartLine == 0 || nxt.StartLine < el.StartLine) {
					el.StartLine = nxt.StartLine
				}
				out = append(out[:i+1], out[i+2:]...)
			}
		}
		i++
	}
	return out
}

func hasHeading(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if HEADING_PATTERN.MatchString(strings.TrimSpace(line)) {
			return true
		}
	}
	return false
}

// -- semi-parse helpers (used by handlers) --

// FindAbstract locates the abstract section. Returns (text, ids, startIdx, endIdx).
func FindAbstract(elements []Element) (text string, ids map[int]bool, startIdx int, endIdx int) {
	ids = make(map[int]bool)
	var lines []string
	var elems []Element
	found := false
	var abLevel int
	hasAbLevel := false

	for _, el := range elements {
		stripped := strings.TrimSpace(el.Text)
		isTitle := el.Type == "title"
		if isTitle && ABSTRACT_PATTERN.MatchString(stripped) && !found {
			found = true
			raw := el.Metadata["level"]
			if lvl, ok := raw.(int); ok {
				abLevel = lvl
				hasAbLevel = true
			}
			lines = append(lines, stripped)
			ids[el.ID] = true
			elems = append(elems, el)
			continue
		}
		if found {
			if isTitle {
				raw := el.Metadata["level"]
				lvl, _ := raw.(int)
				if hasAbLevel && lvl <= abLevel {
					break
				}
			}
			lines = append(lines, stripped)
			ids[el.ID] = true
			elems = append(elems, el)
		}
	}

	text = strings.TrimSpace(strings.Join(lines, "\n"))
	for _, e := range elems {
		if e.StartLine > 0 && (startIdx == 0 || e.StartLine < startIdx) {
			startIdx = e.StartLine
		}
		if e.EndLine > endIdx {
			endIdx = e.EndLine
		}
	}
	return text, ids, startIdx, endIdx
}

// SplitTableText splits a Table element's text by separator; returns (caption, body, footer).
func SplitTableText(text, separator string) (caption, body, footer string) {
	parts := splitBySeparator(text, separator)

	// prefer HTML table
	for idx, part := range parts {
		if HTML_TABLE_OPEN.MatchString(part) {
			if idx > 0 {
				caption = strings.TrimSpace(strings.Join(parts[:idx], "\n"))
			}
			if idx+1 < len(parts) {
				footer = strings.TrimSpace(strings.Join(parts[idx+1:], "\n"))
			}
			return caption, strings.TrimSpace(part), footer
		}
	}

	// then try Markdown table
	for idx, part := range parts {
		s := strings.TrimSpace(part)
		if strings.HasPrefix(s, "|") {
			bodyLines := strings.Split(s, "\n")
			if len(bodyLines) > 0 && MD_TABLE_ROW.MatchString(strings.TrimSpace(bodyLines[len(bodyLines)-1])) {
				if idx > 0 {
					caption = strings.TrimSpace(strings.Join(parts[:idx], "\n"))
				}
				if idx+1 < len(parts) {
					footer = strings.TrimSpace(strings.Join(parts[idx+1:], "\n"))
				}
				var filtered []string
				for _, line := range bodyLines {
					if strings.TrimSpace(line) != "" {
						filtered = append(filtered, line)
					}
				}
				return caption, strings.Join(filtered, "\n"), footer
			}
		}
	}

	return "", "", ""
}

func splitBySeparator(text, sep string) []string {
	if sep == "" {
		return []string{text}
	}
	return strings.Split(text, sep)
}

// SplitFigureText splits a Figure element's text by separator; returns (description, imageMarkdown, hasImage).
func SplitFigureText(text, separator string) (description, imageMarkdown string, hasImage bool) {
	parts := splitBySeparator(text, separator)
	imgIdx := -1
	for k, p := range parts {
		if IMAGE_PATTERN.MatchString(p) {
			imgIdx = k
			break
		}
	}
	if imgIdx == -1 || len(parts) == 1 {
		return text, "", false
	}
	imageMarkdown = parts[imgIdx]
	var others []string
	for k, p := range parts {
		if k != imgIdx {
			if strings.TrimSpace(p) != "" {
				others = append(others, p)
			}
		}
	}
	description = strings.Join(others, "\n")
	return description, imageMarkdown, true
}
