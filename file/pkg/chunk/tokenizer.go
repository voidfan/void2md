package chunk

import (
	"container/list"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// LRUCache 实现简单的LRU缓存
type LRUCache struct {
	capacity int
	ttl      time.Duration
	cache    map[uint64]*list.Element
	list     *list.List
	mutex    sync.RWMutex
}

type cacheEntry struct {
	key       uint64
	count     int
	ts        int64 // Unix nano
	value     string
}

// NewLRUCache 创建新的LRU缓存
func NewLRUCache(capacity int, ttl time.Duration) *LRUCache {
	return &LRUCache{
		capacity: capacity,
		ttl:      ttl,
		cache:    make(map[uint64]*list.Element),
		list:     list.New(),
	}
}

// Get 从缓存获取值
func (c *LRUCache) Get(key uint64) (int, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if element, ok := c.cache[key]; ok {
		entry := element.Value.(*cacheEntry)
		if time.Now().UnixNano()-entry.ts > c.ttl.Nanoseconds() {
			// 缓存过期
			c.list.Remove(element)
			delete(c.cache, key)
			return 0, false
		}
		c.list.MoveToFront(element)
		return entry.count, true
	}
	return 0, false
}

// Put 向缓存添加值
func (c *LRUCache) Put(key uint64, count int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if element, ok := c.cache[key]; ok {
		c.list.MoveToFront(element)
		element.Value.(*cacheEntry).count = count
		element.Value.(*cacheEntry).ts = time.Now().UnixNano()
		return
	}

	if c.list.Len() >= c.capacity {
		// 移除最旧的元素
		back := c.list.Back()
		if back != nil {
			oldEntry := back.Value.(*cacheEntry)
			delete(c.cache, oldEntry.key)
			c.list.Remove(back)
		}
	}

	entry := &cacheEntry{
		key:   key,
		count: count,
		ts:    time.Now().UnixNano(),
	}
	element := c.list.PushFront(entry)
	c.cache[key] = element
}

// -- 全局 tokenizer 状态（sync.Once 保证单例初始化） --

var (
	tokenizerPath   string
	tokenizerInited atomic.Bool
	tokenizerOnce   sync.Once
)

// tlenCache 使用 LRU 缓存实现 token 计数缓存
var tlenCache = NewLRUCache(5000, 5*time.Minute)

// InitTokenizer 初始化 tokenizer（纯 Go fallback 版本仅记录路径，实际不做加载）。
// 在 CGO 版本中此函数会加载 HuggingFace tokenizer。
func InitTokenizer(path string) error {
	tokenizerOnce.Do(func() {
		tokenizerPath = path
		tokenizerInited.Store(true)
	})
	return nil
}

// CountTokens 估算文本的 token 数量。
// 纯 Go fallback：中文字符 ~1 token/char，英文约 1.3 token/word。
func CountTokens(text string) int {
	if text == "" {
		return 0
	}
	// 使用 strings.Fields 分割单词，比逐字符遍历更高效
	words := len(strings.Fields(text))
	runes := utf8.RuneCountInString(text)
	nonASCII := runes - words
	if nonASCII < 0 {
		nonASCII = 0
	}
	// 中文 ~1 token/char，英文 ~1.3 token/word
	est := nonASCII + int(float64(words)*1.3)
	if est < 1 {
		est = 1
	}
	return est
}

// AddTokens 增量计算token数，避免重复计算已知部分
func AddTokens(baseTokens int, newText string) int {
	if newText == "" {
		return baseTokens
	}
	return baseTokens + CountTokens(newText)
}

// TLen 计算文本 token 数（带 LRU 缓存）。
func TLen(text string) int {
	if text == "" {
		return 0
	}
	h := hashText(text)
	if count, ok := tlenCache.Get(h); ok {
		return count
	}
	count := CountTokens(text)
	tlenCache.Put(h, count)
	return count
}

func hashText(text string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(text))
	return h.Sum64()
}

// CountTokensBatch 批量估算多个文本的 token 数。
// 纯 Go fallback 版本与 CountTokens 逐条等价；cgo 版本合并为一次 FFI 调用。
func CountTokensBatch(texts []string) []int {
	n := len(texts)
	if n == 0 {
		return nil
	}
	out := make([]int, n)
	for i, t := range texts {
		out[i] = CountTokens(t)
	}
	return out
}

// TLenBatch 批量计算多个文本的 token 数，语义与逐个 TLen 完全一致（带 LRU 缓存）。
func TLenBatch(texts []string) []int {
	n := len(texts)
	if n == 0 {
		return nil
	}
	out := make([]int, n)
	var miss []int
	for i, t := range texts {
		if t == "" {
			out[i] = 0
			continue
		}
		h := hashText(t)
		if c, ok := tlenCache.Get(h); ok {
			out[i] = c
		} else {
			miss = append(miss, i)
		}
	}
	if len(miss) > 0 {
		missTexts := make([]string, len(miss))
		for k, idx := range miss {
			missTexts[k] = texts[idx]
		}
		counts := CountTokensBatch(missTexts)
		for k, idx := range miss {
			out[idx] = counts[k]
			tlenCache.Put(hashText(texts[idx]), counts[k])
		}
	}
	return out
}

// HardTokenSplit 当所有分隔符都失败时的最终兜底切分。
// 按字符数（近似 token 边界）切分。
func HardTokenSplit(text string, budget int) []string {
	if budget <= 0 {
		return nil
	}
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if len(runes) <= budget {
		return []string{text}
	}
	var out []string
	for i := 0; i < len(runes); i += budget {
		end := i + budget
		if end > len(runes) {
			end = len(runes)
		}
		piece := string(runes[i:end])
		if piece != "" {
			out = append(out, piece)
		}
	}
	return out
}