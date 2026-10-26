package chunk

import (
	"math"
	"regexp"
	"strings"
)

// -- 辅助函数 --

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

// ExtractAbstract 提取摘要：独立成 chunk(s)，标记 abstractIDs。
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

// HandleTitle 处理标题元素。返回需跳过的后续元素数。
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
			// 同级或更高 → 发射，开新 chunk
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
			// 更深级子标题 → 合并
			b.Append(el.Text, el)
			b.CurrentTitle = el
			b.CurrentTitleLevel = curLevel
		}
	} else {
		// 空 chunk：起新 chunk + 注入前缀
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

	// 向前扫描：合并连续的更深级子标题
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

// HandleText 处理文本元素。
func HandleText(ctx *ProcessContext, el *Element) {
	b := ctx.Builder
	b.Track(el)
	cs := ctx.ChunkSizes["text"]
	mergeCS := ctx.MergeBudget("text")

	// 使用 builder 的 CurrentTitle 保持文档层次结构上下文
	currentTitle := b.CurrentTitle

	// 能合并 → 直接追加
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

	// 需要切分 → 构造前缀
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
			// 确保新chunk包含标题前缀
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

// HandleCode 处理代码块元素。
func HandleCode(ctx *ProcessContext, el *Element) {
	b := ctx.Builder
	b.Track(el)
	text := el.Text
	elTok := TLen(text)
	cs := ctx.ChunkSizes["code"]
	mergeCS := ctx.MergeBudget("code")

	// 超限：发射当前 + 切分 + 每片带前缀
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

	// 在预算内：能合并就合并
	if b.Tokens()+elTok <= mergeCS {
		b.Append(text, el)
		return
	}

	// 装不下 → 发射当前，新 chunk = 前缀 + code
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

// HandleFigure 处理图片元素。
func HandleFigure(ctx *ProcessContext, el *Element) {
	b := ctx.Builder
	b.Track(el)
	mergeCS := ctx.MergeBudget("figure")

	image, desc, _ := SplitFigureText(el.Text, ctx.Separator)
	fullTok := TLen(image) + TLen(desc)

	// 全部能合并到当前 chunk
	if b.Tokens()+fullTok <= mergeCS {
		if image != "" {
			b.Append(image, el)
		}
		if desc != "" {
			b.Append(desc, el)
		}
		return
	}

	// 装不下 → 发射当前
	if !b.IsEmpty() {
		b.Emit(ctx.Sink, nil)
	}

	parents := ParentsOf(ctx.Elements, b.CurrentTitle)
	pre := BuildPrefix(ctx, b.CurrentTitle, parents, "figure")

	// 前缀 + 图 + 描述能放下 → 一个 chunk
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

	// 图片优先：先放 prefix + image，描述按句追加
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

// HandleTable 处理表格元素。
// HandleTable 处理表格元素。
func HandleTable(ctx *ProcessContext, el *Element) {
	b := ctx.Builder
	b.Track(el)
	cs := ctx.ChunkSizes["table"]
	mergeCS := ctx.MergeBudget("table")
	isHTML := el.Metadata["format"] == "html"

	// --- 格式相关：解析 + 退化处理 ---
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

	// 使用 builder 的 CurrentTitle 保持文档层次结构上下文
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

	// --- 共同主路径 ---
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

// Dispatch 根据元素类型路由到对应 handler，返回 skip 计数。
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

// -- 内部辅助函数 --

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