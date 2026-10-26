package chunk

import (
	"regexp"
	"sort"
	"strings"
)

// ensureBlockBlankLines 在 HTML block 与 Markdown 内容之间补空行。
//
// 对齐 Python chunker._ensure_block_blank_lines：CommonMark 中 <table>/<details>/
// <summary> 会开启 HTML block——block 内内容不被当作 Markdown 解析，且 HTML block
// 一直延续到空行才结束。若表格/图片/折叠面板与相邻 Markdown（表格、标题、分隔符、
// 段落加粗）之间只有一个 \n 而没有空行，后续 Markdown 会被 HTML block 吞掉或加粗
// 渲染失败。这里把相关标签与图片的相邻换行运行统一规整为 \n\n（幂等）。
//
// 应用位置在 Chunk() 的 PostProcess 之后，仅对非 code 类型 chunk 应用：
// 围栏代码块内的 ![](url) 是字面量，不应改动。
//
// 实现说明：原实现依次跑 6 个正则 ReplaceAllString，每个 pattern 都是
// "<标记或边界字符>(\n+)<边界字符或标记>" 的统一形状，替换为 $1\n\n$3。
//
// 性能优化（输出逐字节不变）：
// 1) 原实现每个 pass 都要 ReplaceAllString 重建一份完整字符串（6 份拷贝）；
//    这里改为对每个正则独立 FindAllStringSubmatchIndex 收集需要规整的
//    换行运行（run），最后一次性重建（0~1 份拷贝）。
// 2) 每个 pass 只在该类 marker 确实出现在文本中时才运行，避免无谓全量扫描。
//
// 正确性依据：
// - 每个正则的 FindAllStringSubmatchIndex 与 ReplaceAllString 内部使用的
//   匹配序列完全相同（都是「左到右、非重叠、从 match 末尾续扫」），因此
//   pass 内部语义（一个匹配吞掉的边界字符/后续 marker 不会被同 pass 再次
//   使用）与原先逐字一致。
// - 各 pass 只改动 run（把 \n+ 规整为 \n\n），不改动 run 两侧的标记与
//   边界字符；而 run 是否被某个正则命中只取决于原文中该 run 两侧的相邻
//   字符，故各 pass 在原文上的匹配与在先前 pass 改动后的文本上的匹配一致
//   ——对要规整的 run 集合求并集即等价于 6 次顺序替换。
// - run 是最大 \n+ 段，互不相交；同一 run 被多个正则命中时规整结果相同
//   （幂等），去重不影响输出。
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

// collectBlockBlankRuns 收集所有需要规整的换行运行区间（原文坐标）。
// 各正则仅在该类 marker 确实存在于文本中时才运行；返回的区间已按起点升序
// 去重（同一 run 被多个正则命中时只保留一个）。
func collectBlockBlankRuns(text, lowered string) [][2]int {
	var runs [][2]int
	appendRuns := func(re *regexp.Regexp) {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			// 组 2 即 run。
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
	// 各正则块的匹配各自升序，跨块需整体排序；最大 run 互不相交，起点相同
	// 即为同一 run，去重后交给 ensureBlockBlankLines 升序重建。
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

// ── 正则（对齐 Python _HTML_*_AFTER/_BEFORE；IMG 无需 IGNORECASE，! 字面匹配）──

// htmlTableAfter 匹配 </table> 之后紧贴非空字符（无空行分隔）。
var htmlTableAfter = regexp.MustCompile(`(?i)(</table\s*>)(\n+)(\S)`)

// htmlTableBefore 匹配非空字符之后紧贴 <table>（无空行分隔）。
var htmlTableBefore = regexp.MustCompile(`(?i)(\S)(\n+)(<table\b)`)

// htmlImgAfter 匹配 Markdown 图片之后紧贴非空字符。
var htmlImgAfter = regexp.MustCompile(`(!\[[^\]]*\]\([^)]+\))(\n+)(\S)`)

// htmlImgBefore 匹配非空字符之后紧贴 Markdown 图片。
var htmlImgBefore = regexp.MustCompile(`(\S)(\n+)(!\[[^\]]*\]\()`)

// htmlDetailsAfter 匹配 </?details|summary> 之后紧贴非空字符（HTML block 开闭边界）。
var htmlDetailsAfter = regexp.MustCompile(`(?i)(</?(?:details|summary)\b[^>]*>)(\n+)(\S)`)

// htmlDetailsBefore 匹配非空字符之后紧贴 </?details|summary>。
var htmlDetailsBefore = regexp.MustCompile(`(?i)(\S)(\n+)(</?(?:details|summary)\b[^>]*>)`)