package chunk

import (
	"math"
	"strings"
)

// PostProcess: split oversize → reclaim orphan prefixes → merge undersize → reindex.
func PostProcess(chunks []Chunk, chunkSizes map[string]int, minRatio, maxRatio float64) []Chunk {
	if len(chunks) == 0 {
		return nil
	}
	if minRatio <= 0 {
		minRatio = 0.3
	}
	if maxRatio <= 0 {
		maxRatio = 1.2
	}

	for iter := 0; iter < 3; iter++ {
		chunks = splitLong(chunks, chunkSizes, maxRatio)
		chunks = redistributeOrphans(chunks, chunkSizes, maxRatio)
		chunks = mergeShort(chunks, chunkSizes, minRatio, maxRatio)
	}
	return reindex(chunks)
}

// -- threshold helpers --

func maxSize(c *Chunk, sizes map[string]int, ratio float64) int {
	if ratio <= 0 {
		return math.MaxInt32
	}
	base, ok := sizes[c.Type]
	if !ok {
		base = sizes["text"]
	}
	return int(math.Round(ratio * float64(base)))
}

func minSize(c *Chunk, sizes map[string]int, ratio float64) int {
	base, ok := sizes[c.Type]
	if !ok {
		base = sizes["text"]
	}
	return int(math.Round(ratio * float64(base)))
}

// -- Phase 1: split oversize chunks --

func splitLong(chunks []Chunk, sizes map[string]int, ratio float64) []Chunk {
	var out []Chunk
	for _, c := range chunks {
		ms := maxSize(&c, sizes, ratio)
		if TLen(c.Text) <= ms {
			out = append(out, c)
			continue
		}
		prefixLines := ExtractHeadingPrefix(c.Text)
		prefix := strings.Join(prefixLines, "\n")
		prefixTok := TLen(prefix)
		budget := ms
		if prefixTok < ms/2 {
			budget = ms - prefixTok
		}

		var parts []string
		if c.Type == "table" && len(prefixLines) > 0 {
			bodyText := strings.Join(strings.Split(c.Text, "\n")[len(prefixLines):], "\n")
			parts = SplitTable(bodyText, budget)
		} else if c.Type == "table" {
			parts = SplitTable(c.Text, budget)
		} else {
			parts = SplitText(c.Text, budget)
		}

		for _, part := range parts {
			if strings.TrimSpace(part) == "" {
				continue
			}
			if TLen(part) > budget && c.Type == "table" {
				for _, sp := range SplitText(part, budget) {
					if strings.TrimSpace(sp) != "" {
						out = append(out, cloneWithPrefix(c, prefix, sp))
					}
				}
			} else {
				out = append(out, cloneWithPrefix(c, prefix, part))
			}
		}
	}
	return out
}

func cloneWithPrefix(c Chunk, prefix, body string) Chunk {
	body = strings.TrimSpace(body)
	text := body
	if prefix != "" && !strings.HasPrefix(body, prefix) {
		// Heading-level dedupe: if body's first line is a heading with level <= the
		// prefix's last heading level, drop that prefix line (usually a duplicated
		// parent heading) to avoid inverted hierarchy.
		bodyFirst := strings.Split(body, "\n")[0]
		if bodyM := HEADING_PATTERN.FindStringSubmatch(strings.TrimSpace(bodyFirst)); bodyM != nil {
			bodyLevel := len(bodyM[1])
			prefixLines := strings.Split(prefix, "\n")
			if len(prefixLines) > 0 {
				lastM := HEADING_PATTERN.FindStringSubmatch(strings.TrimSpace(prefixLines[len(prefixLines)-1]))
				if lastM != nil && bodyLevel <= len(lastM[1]) {
					prefix = strings.Join(prefixLines[:len(prefixLines)-1], "\n")
				}
			}
		}
		if prefix != "" {
			text = prefix + "\n" + body
		}
	}
	return Chunk{
		Index:     0,
		Text:      text,
		StartLine: c.StartLine,
		EndLine:   c.EndLine,
		Type:      c.Type,
	}
}

// -- Phase 2: orphan prefix reclamation --

func crossesSection(text string) bool {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	i := 0
	for i < len(lines) && SECTION_HEADING.MatchString(lines[i]) {
		i++
	}
	for j := i; j < len(lines); j++ {
		if SECTION_HEADING.MatchString(lines[j]) {
			return true
		}
	}
	return false
}

func deepestHeading(text string) string {
	pre := ExtractHeadingPrefix(text)
	if len(pre) > 0 {
		return pre[len(pre)-1]
	}
	return ""
}

func redistributeOrphans(chunks []Chunk, sizes map[string]int, maxRatio float64) []Chunk {
	if maxRatio <= 0 || len(chunks) < 2 {
		return chunks
	}

	out := []Chunk{chunks[0]}
	for _, cur := range chunks[1:] {
		prev := &out[len(out)-1]
		lines := strings.Split(cur.Text, "\n")
		firstH := -1
		for j, line := range lines {
			if SECTION_HEADING.MatchString(strings.TrimSpace(line)) {
				firstH = j
				break
			}
		}
		if firstH <= 0 {
			out = append(out, cur)
			continue
		}

		naked := strings.TrimSpace(strings.Join(lines[:firstH], "\n"))
		if naked == "" {
			out = append(out, cur)
			continue
		}

		rest := strings.Join(lines[firstH:], "\n")
		if deepestHeading(rest) != deepestHeading(prev.Text) {
			out = append(out, cur)
			continue
		}

		newPrevText := strings.TrimRight(prev.Text, "\n") + "\n\n" + naked
		if TLen(newPrevText) > maxSize(prev, sizes, maxRatio) {
			out = append(out, cur)
			continue
		}

		out[len(out)-1] = Chunk{
			Index:     prev.Index,
			Text:      newPrevText,
			StartLine: prev.StartLine,
			EndLine:   maxNonZero(prev.EndLine, cur.StartLine),
			Type:      prev.Type,
		}
		out = append(out, Chunk{
			Index:     cur.Index,
			Text:      rest,
			StartLine: cur.StartLine,
			EndLine:   cur.EndLine,
			Type:      cur.Type,
		})
	}
	return out
}

// -- Phase 3: merge undersize chunks --

func nodeMatch(curText, otherText, fallback string) bool {
	curH := deepestHeading(curText)
	if curH == "" {
		curH = fallback
	}
	otherH := deepestHeading(otherText)
	return curH != "" && otherH != "" && curH == otherH
}

func tryMerge(left, right *Chunk, sizes map[string]int, ratio float64) *Chunk {
	if crossesSection(left.Text) && crossesSection(right.Text) {
		return nil
	}
	text := mergeText(left.Text, right.Text)
	allow := maxSize(left, sizes, ratio)
	if m := maxSize(right, sizes, ratio); m > allow {
		allow = m
	}
	if TLen(text) > allow {
		return nil
	}
	return &Chunk{
		Index:     0,
		Text:      text,
		StartLine: minNonZero(left.StartLine, right.StartLine),
		EndLine:   maxNonZero(left.EndLine, right.EndLine),
		Type:      mergeType(left.Type, right.Type),
	}
}

func mergeShort(chunks []Chunk, sizes map[string]int, minRatio, maxRatio float64) []Chunk {
	var out []Chunk
	i := 0
	for i < len(chunks) {
		cur := chunks[i]
		if TLen(cur.Text) >= minSize(&cur, sizes, minRatio) {
			out = append(out, cur)
			i++
			continue
		}

		consumed := false
		for TLen(cur.Text) < minSize(&cur, sizes, minRatio) {
			var prev, nxt *Chunk
			if len(out) > 0 {
				prev = &out[len(out)-1]
			}
			if i+1 < len(chunks) {
				nxt = &chunks[i+1]
			}

			var left, right *Chunk
			if prev != nil {
				left = tryMerge(prev, &cur, sizes, maxRatio)
			}
			if nxt != nil {
				right = tryMerge(&cur, nxt, sizes, maxRatio)
			}

			if left == nil && right == nil {
				break
			}

			direction := ""
			if left != nil && right != nil {
				fallback := deepestHeading(prev.Text)
				leftSame := nodeMatch(cur.Text, prev.Text, fallback)
				rightSame := nodeMatch(cur.Text, nxt.Text, fallback)
				if leftSame && !rightSame {
					direction = "left"
				} else if rightSame && !leftSame {
					direction = "right"
				} else if deepestHeading(cur.Text) == "" {
					direction = "left"
				} else if TLen(prev.Text) <= TLen(nxt.Text) {
					direction = "left"
				} else {
					direction = "right"
				}
			} else if left != nil {
				direction = "left"
			} else {
				direction = "right"
			}

			if direction == "left" {
				out[len(out)-1] = *left
				i++
				consumed = true
				break
			}
			cur = *right
			i++
		}

		if !consumed {
			out = append(out, cur)
			i++
		}
	}
	return out
}

// -- text/type/line merge helpers --

func mergeText(left, right string) string {
	leftPre := ExtractHeadingPrefix(left)
	rightPre := ExtractHeadingPrefix(right)
	if len(leftPre) == 0 || len(rightPre) == 0 {
		return left + "\n\n" + right
	}

	// find the longest subsequence of leftPre within rightPre
	bestCommon := 0
	bestStart := 0
	for start := 0; start < len(rightPre); start++ {
		common := 0
		li := 0
		for ri := start; ri < len(rightPre); ri++ {
			found := false
			for li < len(leftPre) {
				if leftPre[li] == rightPre[ri] {
					common++
					li++
					found = true
					break
				}
				li++
			}
			if !found {
				break
			}
		}
		if common > bestCommon {
			bestCommon = common
			bestStart = start
		}
	}

	if bestCommon == 0 {
		return left + "\n\n" + right
	}

	removeCount := bestStart + bestCommon
	rest := strings.TrimSpace(right)
	for k := 0; k < removeCount; k++ {
		idx := strings.Index(rest, "\n")
		if idx == -1 {
			rest = ""
			break
		}
		rest = rest[idx+1:]
	}
	if rest != "" {
		return left + "\n\n" + rest
	}
	return left + "\n\n" + right
}

func mergeType(a, b string) string {
	for _, t := range []string{"title", "figure", "table", "code", "text"} {
		if t == "title" {
			continue
		}
		if a == t || b == t {
			return t
		}
	}
	return "text"
}

func minNonZero(a, b int) int {
	var nz []int
	if a > 0 {
		nz = append(nz, a)
	}
	if b > 0 {
		nz = append(nz, b)
	}
	if len(nz) == 0 {
		return 0
	}
	m := nz[0]
	for _, v := range nz {
		if v < m {
			m = v
		}
	}
	return m
}

func maxNonZero(a, b int) int {
	m := a
	if b > m {
		m = b
	}
	return m
}

// -- Phase 4: reindex --

func reindex(chunks []Chunk) []Chunk {
	out := make([]Chunk, len(chunks))
	for i, c := range chunks {
		out[i] = Chunk{
			Index:     i,
			Text:      c.Text,
			StartLine: c.StartLine,
			EndLine:   c.EndLine,
			Type:      c.Type,
		}
	}
	return out
}
