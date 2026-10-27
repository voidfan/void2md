package chunk

import (
	"math"
	"regexp"
	"strings"
)

// -- Helpers --

func onlyHeadings(text string) bool {
	if text == "" {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		if !HEADING_PATTERN.MatchString(s) {
			return false
		}
	}
	return true
}

func stripTrailingHeadings(text string) string {
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && HEADING_PATTERN.MatchString(strings.TrimSpace(lines[len(lines)-1])) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// -- ExtractAbstract --

// ExtractAbstract extracts the abstract into its own chunk(s) and marks abstractIDs.
func ExtractAbstract(ctx *ProcessContext) {
	text, ids, s, e := FindAbstract(ctx.Elements)
	ctx.AbstractIDs = ids
	if text == "" {
		return
	}
	cs := ctx.ChunkSizes["text"]
	if TLen(text) <= cs {
		ctx.Sink.Push(Chunk{Text: text, Type: "text", StartLine: s, EndLine: e})
	} else {
		for _, part := range SplitText(text, cs) {
			ctx.Sink.Push(Chunk{Text: part, Type: "text", StartLine: s, EndLine: e})
		}
	}
}

// -- handleTitle --

// HandleTitle processes a title element; returns the number of following elements to skip.
func HandleTitle(ctx *ProcessContext, idx int) int {
	el := &ctx.Elements[idx]
	b := ctx.Builder
	budget := ctx.MergeBudget("text")

	raw := el.Metadata["level"]
	curLevel, _ := raw.(int)

	if b.CurrentTitle == nil {
		b.CurrentTitle = el
	}

	if !b.IsEmpty() {
		curTitleLevel := b.CurrentTitleLevel
		if curTitleLevel == 0 {
			if raw2, ok := b.CurrentTitle.Metadata["level"]; ok {
				curTitleLevel, _ = raw2.(int)
			}
		}

		if curLevel <= curTitleLevel {
			// same or higher level → emit and start a new chunk
			if onlyHeadings(b.Text) {
				orphan := b.Text
				b.Reset()
				b.CurrentTitle = el
				b.CurrentTitleLevel = curLevel
				b.Replace(el.Text, el)
				b.Prepend(orphan)
			} else {
				b.Emit(ctx.Sink, nil)
				b.CurrentTitle = el
				b.CurrentTitleLevel = curLevel
				b.Replace(el.Text, el)
				parents := ParentsOf(ctx.Elements, el)
				for i := len(parents) - 1; i >= 0; i-- {
					p := parents[i].Text
					if p != "" && TLen(p)+b.Tokens() < budget {
						b.Prepend(p)
					}
				}
			}
		} else {
			// deeper sub-heading → merge
			b.Append(el.Text, el)
			b.CurrentTitle = el
			b.CurrentTitleLevel = curLevel
		}
	} else {
		// empty chunk: start new chunk + inject prefix
		b.CurrentTitle = el
		b.CurrentTitleLevel = curLevel
		b.Replace(el.Text, el)
		parents := ParentsOf(ctx.Elements, el)
		for i := len(parents) - 1; i >= 0; i-- {
			p := parents[i].Text
			if p != "" && TLen(p)+b.Tokens() < budget {
				b.Prepend(p)
			}
		}
	}

	// scan ahead: merge consecutive deeper sub-headings
	skip := 0
	for j := idx + 1; j < len(ctx.Elements); j++ {
		nxt := ctx.Elements[j]
		if ctx.AbstractIDs[nxt.ID] {
			break
		}
		nxtRaw := nxt.Metadata["level"]
		nxtLevel, _ := nxtRaw.(int)
		if nxt.Type != "title" || nxtLevel <= curLevel {
			break
		}
		if !b.IsEmpty() && TLen(nxt.Text)+b.Tokens() >= budget {
			if !onlyHeadings(b.Text) {
				b.Emit(ctx.Sink, nil)
			}
			break
		}
		if strings.TrimSpace(nxt.Text) != "" {
			b.Append(nxt.Text, &nxt)
		}
		b.CurrentTitle = &nxt
		b.CurrentTitleLevel = nxtLevel
		skip++
	}
	return skip
}

// -- handleText --

// HandleText processes a text element.
func HandleText(ctx *ProcessContext, el *Element) {
	b := ctx.Builder
	b.Track(el)
	cs := ctx.ChunkSizes["text"]
	mergeCS := ctx.MergeBudget("text")

	// use the builder's CurrentTitle to keep document hierarchy context
	currentTitle := b.CurrentTitle

	// fits → append directly
	if b.Tokens()+TLen(el.Text) <= mergeCS {
		if b.IsEmpty() {
			parents := ParentsOf(ctx.Elements, currentTitle)
			pre := BuildPrefix(ctx, currentTitle, parents, "text")
			if pre != "" {
				b.Prepend(pre)
			}
		}
		b.Append(el.Text, el)
		return
	}

	// needs splitting → build prefix
	parents := ParentsOf(ctx.Elements, currentTitle)
	pre := BuildPrefix(ctx, currentTitle, parents, "text")
	preTok := TLen(pre)
	usePre := true
	splitSize := cs
	if preTok >= cs/2 {
		splitSize = cs
		usePre = false
	} else {
		splitSize = cs - preTok
		usePre = true
	}

	for _, sentence := range SplitText(el.Text, splitSize) {
		if b.Tokens()+TLen(sentence) <= mergeCS {
			if b.IsEmpty() && pre != "" && usePre {
				b.Prepend(pre)
			}
			b.Append(sentence, el)
		} else {
			b.Text = stripTrailingHeadings(b.Text)
			b.Emit(ctx.Sink, el)
			// ensure the new chunk carries the heading prefix
			if pre != "" {
				b.Prepend(pre)
				b.Append(sentence, el)
			} else {
				b.Replace(sentence, el)
			}
		}
	}
}

// -- handleCode --

// HandleCode processes a code block element.
func HandleCode(ctx *ProcessContext, el *Element) {
	b := ctx.Builder
	b.Track(el)
	text := el.Text
	elTok := TLen(text)
	cs := ctx.ChunkSizes["code"]
	mergeCS := ctx.MergeBudget("code")

	// over budget: emit current + split + prefix each part
	if elTok > cs {
		if !b.IsEmpty() {
			b.Text = stripTrailingHeadings(b.Text)
			b.Emit(ctx.Sink, nil)
		}
		parents := ParentsOf(ctx.Elements, b.CurrentTitle)
		pre := BuildPrefix(ctx, b.CurrentTitle, parents, "code")
		for _, part := range SplitText(text, cs) {
			chunkText := part
			if pre != "" {
				chunkText = pre + "\n" + part
			}
			ctx.Sink.Push(Chunk{
				Text:      chunkText,
				Type:      "code",
				StartLine: el.StartLine,
				EndLine:   el.EndLine,
			})
		}
		return
	}

	// within budget: merge if possible
	if b.Tokens()+elTok <= mergeCS {
		b.Append(text, el)
		return
	}

	// doesn't fit → emit current; new chunk = prefix + code
	if !b.IsEmpty() {
		b.Emit(ctx.Sink, nil)
	}
	parents := ParentsOf(ctx.Elements, b.CurrentTitle)
	pre := BuildPrefix(ctx, b.CurrentTitle, parents, "code")
	if pre != "" {
		b.Replace(pre, el)
		b.Append(text, el)
	} else {
		b.Replace(text, el)
	}
}

// -- handleFigure --

// HandleFigure processes an image element.
func HandleFigure(ctx *ProcessContext, el *Element) {
	b := ctx.Builder
	b.Track(el)
	mergeCS := ctx.MergeBudget("figure")

	image, desc, _ := SplitFigureText(el.Text, ctx.Separator)
	fullTok := TLen(image) + TLen(desc)

	// everything fits into the current chunk
	if b.Tokens()+fullTok <= mergeCS {
		if image != "" {
			b.Append(image, el)
		}
		if desc != "" {
			b.Append(desc, el)
		}
		return
	}

	// doesn't fit → emit current
	if !b.IsEmpty() {
		b.Emit(ctx.Sink, nil)
	}

	parents := ParentsOf(ctx.Elements, b.CurrentTitle)
	pre := BuildPrefix(ctx, b.CurrentTitle, parents, "figure")

	// prefix + image + description fit → one chunk
	if TLen(pre)+fullTok <= mergeCS {
		if pre != "" {
			b.Replace(pre, el)
			if image != "" {
				b.Append(image, el)
			}
			if desc != "" {
				b.Append(desc, el)
			}
		} else {
			if image != "" {
				b.Replace(image, el)
				if desc != "" {
					b.Append(desc, el)
				}
			} else if desc != "" {
				b.Replace(desc, el)
			}
		}
		return
	}

	// image first: prefix + image, then append description sentence by sentence
	if pre != "" {
		b.Replace(pre, el)
		b.Append(image, el)
	} else {
		b.Replace(image, el)
	}

	if desc == "" {
		return
	}
	for _, sentence := range SplitText(desc, mergeCS) {
		if strings.TrimSpace(sentence) == "" {
			continue
		}
		if b.Tokens()+TLen(sentence) <= mergeCS {
			b.Append(sentence, el)
		} else {
			b.Emit(ctx.Sink, el)
			parts := []string{}
			if pre != "" {
				parts = append(parts, pre)
			}
			parts = append(parts, image, sentence)
			b.Replace(strings.Join(parts, "\n"), el)
		}
	}
}

// -- handleTable --

// HandleTable processes a table element.
func HandleTable(ctx *ProcessContext, el *Element) {
	b := ctx.Builder
	b.Track(el)
	cs := ctx.ChunkSizes["table"]
	mergeCS := ctx.MergeBudget("table")
	isHTML := el.Metadata["format"] == "html"

	// --- format-specific: parse + degenerate cases ---
	var openTag, header string
	var rows []string
	var preDesc, postDesc, body string
	if isHTML {
		openTag, header, rows, preDesc, postDesc = ParseHTMLTable(el.Text)
		if header == "" && len(rows) == 0 {
			degenerateHTMLAtomic(ctx, el, b, openTag, preDesc, postDesc, cs)
			return
		}
	} else {
		preDesc, body, postDesc = SplitTableText(el.Text, ctx.Separator)
		if body == "" {
			return
		}
	}

	// use the builder's CurrentTitle to keep document hierarchy context
	currentTitle := b.CurrentTitle

	parents := ParentsOf(ctx.Elements, currentTitle)
	preContent := BuildPrefix(ctx, currentTitle, parents, "table")
	preDesc, postDesc = emitOversizeDescriptions(ctx, preDesc, postDesc, el, cs, preContent)

	if isHTML {
		if len(rows) == 0 {
			degenerateHTMLHeaderOnly(ctx, el, b, openTag, header, preDesc, postDesc, cs)
			return
		}
	} else {
		header, rows = ParseMDTable(body)
		if len(rows) == 0 {
			merged := strings.Join(filterNonEmpty([]string{b.Text, preDesc, body, postDesc}), "\n")
			if TLen(merged) <= mergeCS {
				b.Replace(merged, el)
			} else {
				b.Emit(ctx.Sink, nil)
				ctx.Sink.Push(Chunk{
					Text:      strings.Join(filterNonEmpty([]string{preContent, preDesc, body, postDesc}), "\n"),
					Type:      "table",
					StartLine: el.StartLine,
					EndLine:   el.EndLine,
				})
			}
			return
		}
	}

	// --- common main path ---
	wrapFn := func(g []string) string { return "" }
	var overhead int
	if isHTML {
		wrapFn = func(g []string) string {
			return openTag + header + strings.Join(g, "") + "</table>"
		}
		overhead = TLen(preContent) + TLen(preDesc) + TLen(openTag) + TLen(header) + TLen("</table>") + TLen(postDesc)
	} else {
		wrapFn = func(g []string) string {
			return strings.Join(append([]string{header}, g...), "\n")
		}
		overhead = TLen(preContent) + TLen(preDesc) + TLen(header) + TLen(postDesc)
	}

	groups := PackByBudget(rows, cs, overhead)
	if len(groups) == 0 {
		return
	}
	emitTableGroups(ctx, el, groups, preContent, preDesc, postDesc, wrapFn)
}

// -- Dispatch --

// Dispatch routes an element to its handler by type; returns a skip count.
func Dispatch(ctx *ProcessContext, idx int) int {
	el := ctx.Elements[idx]
	if el.Type == "title" {
		return HandleTitle(ctx, idx)
	}
	switch el.Type {
	case "text":
		HandleText(ctx, &el)
	case "code":
		HandleCode(ctx, &el)
	case "figure":
		HandleFigure(ctx, &el)
	case "table":
		HandleTable(ctx, &el)
	default:
		HandleText(ctx, &el)
	}
	return 0
}

// -- internal helpers --

func filterNonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
func emitOversizeDescriptions(ctx *ProcessContext, pre, post string, el *Element, cs int, preContent string) (string, string) {
	descRatio := 0.3
	limit := int(math.Round(float64(cs) * descRatio))
	if TLen(post) > limit {
		for _, p := range SplitText(post, cs) {
			text := strings.Join(filterNonEmpty([]string{preContent, p}), "\n")
			ctx.Sink.Push(Chunk{Text: text, Type: "table", StartLine: el.StartLine, EndLine: el.EndLine})
		}
		post = ""
	}
	if TLen(pre) > limit {
		for _, p := range SplitText(pre, cs) {
			text := strings.Join(filterNonEmpty([]string{preContent, p}), "\n")
			ctx.Sink.Push(Chunk{Text: text, Type: "table", StartLine: el.StartLine, EndLine: el.EndLine})
		}
		pre = ""
	}
	return pre, post
}

func emitTableGroups(ctx *ProcessContext, el *Element, groups [][]string,
	preContent, preDesc, postDesc string, wrapFn func([]string) string) {

	b := ctx.Builder
	mergeCS := ctx.MergeBudget("table")

	for gi, group := range groups {
		var parts []string
		if preContent != "" {
			parts = append(parts, preContent)
		}
		if preDesc != "" {
			parts = append(parts, preDesc)
		}
		parts = append(parts, wrapFn(group))
		if postDesc != "" {
			parts = append(parts, postDesc)
		}
		chunkText := strings.Join(parts, "\n")

		if gi == 0 && !b.IsEmpty() {
			dedupedPre := DedupePrefixForBuilder(preContent, b)
			var merged string
			if dedupedPre != preContent {
				mergedParts := make([]string, len(parts))
				copy(mergedParts, parts)
				if dedupedPre != "" {
					mergedParts[0] = dedupedPre
				} else if preContent != "" {
					mergedParts = mergedParts[1:]
				}
				merged = strings.Join(append([]string{b.Text}, mergedParts...), "\n")
			} else {
				merged = strings.Join([]string{b.Text, chunkText}, "\n")
			}
			if TLen(merged) <= mergeCS {
				startLine := el.StartLine
				endLine := el.EndLine
				if b.LineStart > 0 && b.LineStart < startLine {
					startLine = b.LineStart
				}
				ctx.Sink.Push(Chunk{Text: merged, Type: "table", StartLine: startLine, EndLine: endLine})
				b.Reset()
				continue
			}
			b.Emit(ctx.Sink, nil)
		}

		ctx.Sink.Push(Chunk{Text: chunkText, Type: "table", StartLine: el.StartLine, EndLine: el.EndLine})
	}
}

func degenerateHTMLAtomic(ctx *ProcessContext, el *Element, b *ChunkBuilder,
	body, preDesc, postDesc string, cs int) {

	merged := strings.Join(filterNonEmpty([]string{b.Text, preDesc, body, postDesc}), "\n")
	if TLen(merged) <= cs {
		startLine := el.StartLine
		endLine := el.EndLine
		if b.LineStart > 0 && b.LineStart < startLine {
			startLine = b.LineStart
		}
		ctx.Sink.Push(Chunk{Text: merged, Type: "table", StartLine: startLine, EndLine: endLine})
		b.Reset()
		return
	}
	if !b.IsEmpty() {
		b.Text = stripTrailingHeadings(b.Text)
		b.Emit(ctx.Sink, nil)
	}
	parents := ParentsOf(ctx.Elements, b.CurrentTitle)
	preContent := BuildPrefix(ctx, b.CurrentTitle, parents, "table")
	atomic := strings.Join(filterNonEmpty([]string{preDesc, body, postDesc}), "\n")
	for _, part := range SplitText(atomic, cs) {
		chunk := part
		if preContent != "" {
			chunk = preContent + "\n" + part
		}
		ctx.Sink.Push(Chunk{Text: chunk, Type: "table", StartLine: el.StartLine, EndLine: el.EndLine})
	}
}

func degenerateHTMLHeaderOnly(ctx *ProcessContext, el *Element, b *ChunkBuilder,
	openTag, header, preDesc, postDesc string, cs int) {

	headerOnly := openTag + header + "</table>"
	merged := strings.Join(filterNonEmpty([]string{b.Text, preDesc, headerOnly, postDesc}), "\n")
	if TLen(merged) <= cs {
		b.Replace(merged, el)
		return
	}

	thTagRe := regexp.MustCompile(`(?i)<th\b[^>]*>`)
	thOpenM := thTagRe.FindStringIndex(header)
	thClosePos := strings.LastIndex(header, "</th>")

	if thOpenM == nil || thClosePos == -1 {
		if !b.IsEmpty() {
			b.Text = stripTrailingHeadings(b.Text)
			b.Emit(ctx.Sink, nil)
		}
		parents := ParentsOf(ctx.Elements, b.CurrentTitle)
		preContent := BuildPrefix(ctx, b.CurrentTitle, parents, "table")
		atomic := strings.Join(filterNonEmpty([]string{preDesc, headerOnly, postDesc}), "\n")
		for _, part := range SplitText(atomic, cs) {
			chunk := part
			if preContent != "" {
				chunk = preContent + "\n" + part
			}
			ctx.Sink.Push(Chunk{Text: chunk, Type: "table", StartLine: el.StartLine, EndLine: el.EndLine})
		}
		return
	}

	thOpen := header[:thOpenM[1]]
	thContent := header[thOpenM[1]:thClosePos]
	thClose := header[thClosePos:]

	parents := ParentsOf(ctx.Elements, b.CurrentTitle)
	preContent := BuildPrefix(ctx, b.CurrentTitle, parents, "table")
	prefixTmpl := strings.Join(filterNonEmpty([]string{preContent, preDesc}), "\n")
	if prefixTmpl != "" {
		prefixTmpl += "\n"
	}
	prefixTmpl += openTag + thOpen

	suffix := thClose + "</table>"
	if postDesc != "" {
		suffix += "\n" + postDesc
	}

	overhead := TLen(prefixTmpl) + TLen(suffix)
	budget := cs - overhead
	if budget < 1 {
		budget = 1
	}

	first := true
	for _, content := range SplitText(thContent, budget) {
		if first && !b.IsEmpty() {
			firstPfx := strings.Join(filterNonEmpty([]string{b.Text, preDesc}), "\n")
			if firstPfx != "" {
				firstPfx += "\n"
			}
			firstPfx += openTag + thOpen
			startLine := el.StartLine
			endLine := el.EndLine
			if b.LineStart > 0 && b.LineStart < startLine {
				startLine = b.LineStart
			}
			ctx.Sink.Push(Chunk{Text: firstPfx + content + suffix, Type: "table", StartLine: startLine, EndLine: endLine})
			b.Reset()
		} else {
			ctx.Sink.Push(Chunk{Text: prefixTmpl + content + suffix, Type: "table", StartLine: el.StartLine, EndLine: el.EndLine})
		}
		first = false
	}
}
