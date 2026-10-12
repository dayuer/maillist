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
// of "[a](" takes 77 seconds while the same bytes of prose take a millisecond. Four things keep that from
// being an amplification anybody can trigger:
//
//   - a body is parsed once, when it is saved, and refused if it cannot be rendered inside the budget
//     (CheckRenderable). A body that costs seconds never reaches the database.
//   - a body is parsed once however many requests ask for it at the same time (Render's single flight),
//     and once ever after (the memo). A page a hundred readers open at once is one parse.
//   - at most MaxConcurrentParses *different* bodies may be parsed at once, and a slot is held only
//     until the budget passes — not for as long as the abandoned parse runs, which would make every
//     other render wait minutes behind it.
//   - a parse that passes the budget is a verdict, and it is remembered: that body is text from then on,
//     so the first reader waits a second and every reader after is answered from memory.
//
// goldmark cannot be interrupted, so an abandoned parse does run to its end. What is bounded here is how
// many may be started, and what every later request costs — which is nothing.

// MaxConcurrentParses is how many different bodies may be parsed at once.
//
// Two, because a parse past its budget is one being abandoned, and the number exists to keep a few of
// them from adding up. A slot is held for at most the budget, so a healthy deployment never queues
// behind one.
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

	mu       sync.Mutex
	order    *list.List
	items    map[string]*cacheEntry
	bytes    int
	inflight map[string]*call

	parses  atomic.Int64
	hits    atomic.Int64
	shared  atomic.Int64
	live    atomic.Int64
	peak    atomic.Int64
	orphans atomic.Int64
}

// call is one parse that other renderers are waiting on.
type call struct {
	done     chan struct{}
	rendered string
	within   bool
}

// NewCache returns an empty cache that remembers at most maxBytes of rendered Markdown.
func NewCache(maxBytes int) *Cache {
	return &Cache{
		maxBytes: maxBytes,
		slots:    make(chan struct{}, MaxConcurrentParses),
		order:    list.New(),
		items:    map[string]*cacheEntry{},
		inflight: map[string]*call{},
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
	if inFlight, leader := c.join(key); !leader {
		// Somebody else is parsing this exact body. Waiting for their answer costs nothing and saves a
		// parse: it is what makes a page a hundred readers open at once one parse rather than a
		// hundred, and one abandoned parse rather than a hundred.
		<-inFlight.done
		c.shared.Add(1)
		return inFlight.rendered, inFlight.within
	}

	rendered, within, saturated := c.parseBody(src, isEmail)
	if !saturated {
		// A parse that ran and passed its budget is a verdict about the body, and it is remembered.
		// What is not remembered is having found every slot busy: that is a fact about the moment, and
		// remembering it would turn one busy second into a page that is plain text until the entry is
		// evicted. The next request tries again.
		c.store(key, rendered, within)
	}
	c.publish(key, rendered, within)
	return rendered, within
}

// join either joins the parse already running for this body, or becomes its leader.
func (c *Cache) join(key string) (*call, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if inFlight, ok := c.inflight[key]; ok {
		return inFlight, false
	}
	inFlight := &call{done: make(chan struct{})}
	c.inflight[key] = inFlight
	return inFlight, true
}

// publish hands the leader's answer to everyone waiting on it.
func (c *Cache) publish(key, rendered string, within bool) {
	c.mu.Lock()
	inFlight := c.inflight[key]
	delete(c.inflight, key)
	if inFlight != nil {
		inFlight.rendered, inFlight.within = rendered, within
	}
	c.mu.Unlock()
	if inFlight != nil {
		close(inFlight.done)
	}
}

// Parses is how many bodies this cache has parsed, Hits how many renders it answered from memory, and
// Shared how many it answered by waiting on a parse somebody else had started. The three together are
// the whole story of what the memo did.
func (c *Cache) Parses() int64 { return c.parses.Load() }

// Hits is how many renders the memo answered without parsing.
func (c *Cache) Hits() int64 { return c.hits.Load() }

// Shared is how many renderers waited on a parse that was already running.
func (c *Cache) Shared() int64 { return c.shared.Load() }

// Peak is the most parses that have ever been running at once, and Orphans how many passed their budget
// and were left to finish on their own. The two together are what the cap did.
func (c *Cache) Peak() int64 { return c.peak.Load() }

// Orphans is how many parses passed the budget and finished without anybody waiting for them.
func (c *Cache) Orphans() int64 { return c.orphans.Load() }

// WaitIdle blocks until no parse is inside its budget window, which is what a caller that has to measure
// something needs: with a slot free, the next render is parsed rather than shown as text. ok is false
// when the timeout passed first.
//
// It is about slots, not about abandoned parses: those are past their budget and hold nothing.
func (c *Cache) WaitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(c.slots) == 0 {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return len(c.slots) == 0
}

// parseBody runs one parse: a slot, a goroutine, and a budget.
//
// The slot is taken before the goroutine starts and held until the parse ends — including the ones that
// are abandoned. That is what makes the cap a cap: past MaxConcurrentParses, the parse is not started.
func (c *Cache) parseBody(src string, isEmail bool) (rendered string, within, saturated bool) {
	if !c.acquire() {
		// Every slot is held by a parse that has not finished. Starting another is the unbounded growth
		// this cap exists to stop, so the body is shown as the text somebody wrote.
		return plainText(src), false, true
	}
	c.parses.Add(1)
	c.trackLiveness(1)
	done := make(chan string, 1)
	go func() { done <- convertMarkdown(src, isEmail) }()
	select {
	case rendered := <-done:
		c.trackLiveness(-1)
		c.release()
		return rendered, true, false
	case <-time.After(MarkdownBudget):
		// The slot goes back now, not when the parse eventually ends. A slot held for the length of an
		// abandoned parse is one every other render waits that long for — an outage that is worse than
		// the CPU it would have saved.
		c.trackLiveness(-1)
		c.release()
		c.orphans.Add(1)
		return plainText(src), false, false
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

func (c *Cache) release() { <-c.slots }

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
