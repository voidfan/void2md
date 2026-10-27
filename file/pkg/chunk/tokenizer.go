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

// LRUCache is a simple LRU cache.
type LRUCache struct {
	capacity int
	ttl      time.Duration
	cache    map[uint64]*list.Element
	list     *list.List
	mutex    sync.RWMutex
}

type cacheEntry struct {
	key   uint64
	count int
	ts    int64 // Unix nano
	value string
}

// NewLRUCache creates an LRU cache.
func NewLRUCache(capacity int, ttl time.Duration) *LRUCache {
	return &LRUCache{
		capacity: capacity,
		ttl:      ttl,
		cache:    make(map[uint64]*list.Element),
		list:     list.New(),
	}
}

// Get returns a cached value.
func (c *LRUCache) Get(key uint64) (int, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if element, ok := c.cache[key]; ok {
		entry := element.Value.(*cacheEntry)
		if time.Now().UnixNano()-entry.ts > c.ttl.Nanoseconds() {
			// entry expired
			c.list.Remove(element)
			delete(c.cache, key)
			return 0, false
		}
		c.list.MoveToFront(element)
		return entry.count, true
	}
	return 0, false
}

// Put adds a value to the cache.
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
		// evict oldest element
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

// -- global tokenizer state (sync.Once guarantees singleton init) --

var (
	tokenizerPath   string
	tokenizerInited atomic.Bool
	tokenizerOnce   sync.Once
)

// tlenCache caches token counts via LRU.
var tlenCache = NewLRUCache(5000, 5*time.Minute)

// InitTokenizer initializes the tokenizer. Pure-Go fallback only records the path;
// the CGO version loads the HuggingFace tokenizer here.
func InitTokenizer(path string) error {
	tokenizerOnce.Do(func() {
		tokenizerPath = path
		tokenizerInited.Store(true)
	})
	return nil
}

// CountTokens estimates the token count of text.
// Pure-Go fallback heuristic: CJK ~1 token/char, English ~1.3 token/word.
func CountTokens(text string) int {
	if text == "" {
		return 0
	}
	// strings.Fields word split is faster than per-char iteration
	words := len(strings.Fields(text))
	runes := utf8.RuneCountInString(text)
	nonASCII := runes - words
	if nonASCII < 0 {
		nonASCII = 0
	}
	// non-ASCII ~1 token/char, words ~1.3 tokens each
	est := nonASCII + int(float64(words)*1.3)
	if est < 1 {
		est = 1
	}
	return est
}

// AddTokens increments a token count, avoiding recounting the known part.
func AddTokens(baseTokens int, newText string) int {
	if newText == "" {
		return baseTokens
	}
	return baseTokens + CountTokens(newText)
}

// TLen returns the token count of text (LRU-cached).
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

// CountTokensBatch estimates token counts for multiple texts.
// Pure-Go fallback is equivalent to per-item CountTokens; the cgo version batches
// into one FFI call.
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

// TLenBatch computes token counts for multiple texts, semantically identical to
// per-item TLen (LRU-cached).
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

// HardTokenSplit is the last-resort split when all separators fail:
// splits by rune count (approximating token boundaries).
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
