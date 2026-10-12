package content

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"mailcheap/internal/core/model"
)

// A CommonMark parser has shapes it is quadratic on, and a body may be half a megabyte: measured, 500 KB
// of "[a](" takes 77 seconds while the same bytes of prose take a millisecond. Three things keep that
// from being an amplification anybody can trigger:
//
//   - a body is parsed once, when it is saved, and refused if it cannot be rendered inside the budget
//     (CheckRenderable). A body that costs seconds never reaches the database.
//   - a parse that runs past the budget is abandoned, and only MaxConcurrentParses of those may be
//     running at once: past that, a body is shown as text instead of parsed. This is what stops a body
//     that is already in the database — one saved before this gate existed — from turning every reader
//     of its page and every recipient of its email into another minutes-long goroutine.
//   - identical bodies are parsed once (Cache), which is what makes a page a hundred readers ask for
//     one parse rather than a hundred.
//
// goldmark cannot be interrupted, so what the budget bounds is the waiting. The cap and the memo are
// what bound the work.

// MaxConcurrentParses is how many Markdown parses may be in flight at once.
//
// Two, because a parse past its budget is a parse being abandoned, and the number exists to keep a few
// of them from adding up. A parse that finishes inside the budget holds its slot for microseconds, so
// a healthy deployment never notices the cap; it only bites when parses are already overrunning.
const MaxConcurrentParses = 2

// DefaultCacheBytes is how much rendered Markdown the process remembers, by total size. A body may be
// half a megabyte, so this is a few dozen of them: enough for the issues a deployment is serving, small
// enough not to be a leak.
const DefaultCacheBytes = 16 << 20

// defaultCache is what MarkdownToHTML renders through.
var defaultCache = NewCache(DefaultCacheBytes)

// DefaultCache is the memo every renderer in this process shares. It is exported for its counters: an
// operator asking why an archive page is slow wants to know whether bodies are being parsed again.
func DefaultCache() *Cache { return defaultCache }

// ErrTooComplex is what CheckRenderable returns for a body that cannot be rendered inside the budget.
var ErrTooComplex = errTooComplex{}

type errTooComplex struct{}

func (errTooComplex) Error() string {
	return "this body's Markdown is too complex to render in a second. Simplify the nesting, or use the " +
		"html format, which is not parsed"
}

// CheckRenderable is the write-time gate: it renders a Markdown body the way a page and an email will,
// and refuses one that does not finish inside the budget.
//
// Refusing at the door is the point. A body that takes seconds to parse would take them again for every
// reader of its archive page and every recipient of its email, and that cannot be taken back: the
// deployment would be committed to rendering it, from a public address, as often as anybody asks.
func CheckRenderable(format model.Format, body string) error {
	if format != model.FormatMarkdown {
		// The html format is not parsed: it goes into a frame as it was written.
		return nil
	}
	if _, within := defaultCache.Render(body, true); !within {
		return ErrTooComplex
	}
	return nil
}

// Cache is a bounded memo of rendered Markdown, keyed by the source and the renderer.
//
// It is also where the concurrency cap lives, because the two are one decision: a body that has just
// been rendered is not parsed again, and a body whose parse is still running when the budget passes is
// one of the few the process allows to keep going.
type Cache struct {
	maxBytes int
	slots    chan struct{}

	mu    sync.Mutex
	order *list.List
	items map[string]*cacheEntry
	bytes int

	parses atomic.Int64
	hits   atomic.Int64
	live   atomic.Int64
	peak   atomic.Int64
}

// NewCache returns an empty cache that remembers at most maxBytes of rendered Markdown.
func NewCache(maxBytes int) *Cache {
	return &Cache{
		maxBytes: maxBytes,
		slots:    make(chan struct{}, MaxConcurrentParses),
		order:    list.New(),
		items:    map[string]*cacheEntry{},
	}
}

// Render is MarkdownToHTML through this cache.
//
// within is false when the body was shown as text instead of rendered: its parse ran past the budget,
// or the process was already parsing as much as it allows and this body did not wait for a slot. A
// cached answer reports the same within it was stored with, so a body's fate does not depend on which
// request paid for it.
func (c *Cache) Render(src string, isEmail bool) (rendered string, within bool) {
	key := cacheKey(src, isEmail)
	if entry, ok := c.lookup(key); ok {
		c.hits.Add(1)
		return entry.rendered, entry.within
	}
	rendered, within = c.parseBody(src, isEmail)
	c.store(key, rendered, within)
	return rendered, within
}

// Parses is how many bodies this cache has parsed, and Hits how many renders it answered from memory.
// Together they are the whole story of what the memo did.
func (c *Cache) Parses() int64 { return c.parses.Load() }

// Hits is how many renders the memo answered without parsing.
func (c *Cache) Hits() int64 { return c.hits.Load() }

// Peak is the most parses that have ever been running at once. It is what the concurrency cap is
// asserted against, and what an operator would look at to see the cap working.
func (c *Cache) Peak() int64 { return c.peak.Load() }

// parseBody runs one parse: a slot, a goroutine, and a budget.
//
// The slot is taken before the goroutine starts and held until the parse ends — including the ones that
// are abandoned. That is what makes the cap a cap: past MaxConcurrentParses, the parse is not started.
func (c *Cache) parseBody(src string, isEmail bool) (rendered string, within bool) {
	if !c.acquire() {
		// Every slot is held by a parse that has not finished. Starting another is the unbounded growth
		// this cap exists to stop, so the body is shown as the text somebody wrote.
		return plainText(src), false
	}
	c.parses.Add(1)
	c.trackLiveness(1)
	done := make(chan string, 1)
	go func() {
		defer func() {
			c.trackLiveness(-1)
			<-c.slots
		}()
		done <- convertMarkdown(src, isEmail)
	}()
	select {
	case rendered := <-done:
		return rendered, true
	case <-time.After(MarkdownBudget):
		return plainText(src), false
	}
}

// acquire takes a slot, waiting at most the budget for one to come free.
//
// Waiting the budget is what keeps a healthy deployment out of the fallback: a slot is held for
// microseconds by a parse that finishes, so one comes free almost immediately. It is held for as long as
// the parse takes by the ones that overrun, and when all of the slots are held by those, the answer is
// the body as text.
func (c *Cache) acquire() bool {
	select {
	case c.slots <- struct{}{}:
		return true
	case <-time.After(MarkdownBudget):
		return false
	}
}

// trackLiveness counts the parses running right now, and remembers the high-water mark.
func (c *Cache) trackLiveness(delta int64) {
	live := c.live.Add(delta)
	for {
		peak := c.peak.Load()
		if live <= peak || c.peak.CompareAndSwap(peak, live) {
			return
		}
	}
}

func (c *Cache) lookup(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return cacheEntry{}, false
	}
	c.order.MoveToFront(entry.element)
	return *entry, true
}

func (c *Cache) store(key, rendered string, within bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.items[key]; ok {
		c.order.MoveToFront(existing.element)
		return
	}
	// A rendering larger than the whole budget is not worth evicting everything else for.
	if len(rendered) > c.maxBytes {
		return
	}
	entry := &cacheEntry{key: key, rendered: rendered, within: within}
	entry.element = c.order.PushFront(entry)
	c.items[key] = entry
	c.bytes += len(rendered)
	for c.bytes > c.maxBytes {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		evicted := oldest.Value.(*cacheEntry)
		c.order.Remove(oldest)
		delete(c.items, evicted.key)
		c.bytes -= len(evicted.rendered)
	}
}

type cacheEntry struct {
	key      string
	rendered string
	within   bool
	element  *list.Element
}

func cacheKey(src string, isEmail bool) string {
	sum := sha256.Sum256([]byte(strconv.FormatBool(isEmail) + "\x00" + src))
	return hex.EncodeToString(sum[:])
}
