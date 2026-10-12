package content

import (
	"strings"
	"sync"
	"testing"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/slowinput"
)

// overBudget is a body a CommonMark parser cannot finish inside the budget: a long run of link openers.
func overBudget() string { return slowinput.Repeat("[a](", 100_000) }

// 并发上限：无论多少请求同时撞上一份超预算的正文，同时在跑的解析不超过 MaxConcurrentParses。
//
// 这是「等待有界、CPU 无界」的修法：预算内的解析占槽位只有微秒，真正会长时间占住槽位的是被放弃的
// 那些；槽位满了就不再起新的 goroutine，直接给纯文本。
func TestAbandonedParsesAreCapped(t *testing.T) {
	cache := NewCache(DefaultCacheBytes)
	body := overBudget() // over budget, so every parse that starts is abandoned

	const callers = 50
	start := time.Now()
	var wg sync.WaitGroup
	rendered := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rendered[i], _ = cache.Render(body, true)
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// 断言的是策略本身，写成固定的数字：拿 MaxConcurrentParses 去比就永远测不出这个常量被调大。
	const smallCap = 4
	if MaxConcurrentParses > smallCap {
		t.Fatalf("the cap is %d: it exists to keep a few abandoned parses from adding up",
			MaxConcurrentParses)
	}
	if peak := cache.Peak(); peak > smallCap {
		t.Errorf("%d parses ran at once, want a handful at most", peak)
	}
	if peak := cache.Peak(); peak == 0 {
		t.Error("no parse was recorded at all")
	}
	// 真正的后果：多数调用方根本没有开始解析，而是在槽位被占满时直接拿到纯文本。
	started := cache.Parses()
	if started > smallCap {
		t.Errorf("%d of %d callers started a parse, want only the capped few", started, callers)
	}
	if started == 0 {
		t.Error("no caller parsed at all: the cap would make every body text")
	}
	// 每个调用方都在预算 + 一点余量内拿到答案，而不是排队等被放弃的解析跑完。
	if elapsed > 3*MarkdownBudget {
		t.Errorf("%d callers took %s, want each to be answered within the budget", callers, elapsed)
	}
	for i, got := range rendered {
		if !strings.Contains(got, "&lt;a&gt;") && !strings.Contains(got, "[a](") {
			t.Errorf("caller %d got neither the rendering nor the text: %.40q", i, got)
		}
	}
}

// 同一份正文只解析一次：一百个读者问同一期，是一次解析而不是一百次。
func TestTheSameBodyIsParsedOnce(t *testing.T) {
	cache := NewCache(DefaultCacheBytes)
	body := "# Hello\n\nA paragraph with a [link](https://example.com).\n"

	first, within := cache.Render(body, true)
	if !within {
		t.Fatal("ordinary Markdown was reported as over budget")
	}
	for i := 0; i < 99; i++ {
		again, _ := cache.Render(body, true)
		if again != first {
			t.Fatalf("render %d differs from the first", i)
		}
	}
	if parses := cache.Parses(); parses != 1 {
		t.Errorf("parsed %d times, want 1", parses)
	}
	if hits := cache.Hits(); hits != 99 {
		t.Errorf("hits = %d, want 99", hits)
	}

	// 两种渲染器是两件事：归档的 box 与邮件的 box 不一样，不能用同一份结果。
	cache.Render(body, false)
	if parses := cache.Parses(); parses != 2 {
		t.Errorf("parsed %d times, want the archive renderer to be its own entry", parses)
	}
}

// 超预算的结果也被记住：一次退回纯文本，此后不再为同一份正文花时间。
func TestTheFallbackIsRememberedToo(t *testing.T) {
	cache := NewCache(DefaultCacheBytes)
	body := overBudget()

	_, within := cache.Render(body, true)
	if within {
		t.Fatal("the pathological body was reported as rendered within the budget")
	}
	start := time.Now()
	_, within = cache.Render(body, true)
	if within {
		t.Error("the cached answer forgot that it was a fallback")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("the second render took %s: the fallback was parsed again", elapsed)
	}
	if parses := cache.Parses(); parses != 1 {
		t.Errorf("parsed %d times, want 1", parses)
	}
}

// 缓存有界：超过预算的部分被逐出，且逐出的是最久没用的那些。
func TestTheCacheStaysWithinItsBudget(t *testing.T) {
	// 每条渲染后的正文约 12 字节，装得下三条。
	cache := NewCache(40)
	var bodies []string
	for _, body := range []string{"aaaa", "bbbb", "cccc", "dddd"} {
		bodies = append(bodies, body)
		cache.Render(body, true)
	}
	if parses, hits := cache.Parses(), cache.Hits(); parses != 4 || hits != 0 {
		t.Fatalf("parses = %d hits = %d, want 4 and 0", parses, hits)
	}
	// 最近用的那条还在。
	if _, ok := cache.lookup(cacheKey(bodies[3], true)); !ok {
		t.Error("the most recent entry was evicted")
	}
	// 最久的那条已经走了：再问一次要重新解析。
	cache.Render(bodies[0], true)
	if parses := cache.Parses(); parses != 5 {
		t.Errorf("parses = %d, want the oldest entry to have been evicted", parses)
	}
}

// 写入时的闸门：解析器吃不下的正文被拒绝，普通正文通过，html 格式不看这一条（它本来就不解析）。
func TestCheckRenderableRefusesWhatCannotBeRendered(t *testing.T) {
	if err := CheckRenderable(model.FormatMarkdown, overBudget()); err == nil {
		t.Fatal("a body that cannot be rendered in a second was accepted")
	} else if !strings.Contains(err.Error(), "too complex") {
		t.Errorf("err = %q", err)
	}
	if err := CheckRenderable(model.FormatMarkdown, "# Hello\n\nOrdinary prose with a [link](https://x.test)."); err != nil {
		t.Errorf("ordinary Markdown was refused: %v", err)
	}
	// 大而普通：100 KB 的散文是毫秒级的。
	if err := CheckRenderable(model.FormatMarkdown, strings.Repeat("Some ordinary words. ", 5000)); err != nil {
		t.Errorf("100 KB of prose was refused: %v", err)
	}
	// html 走 frame，不解析。
	if err := CheckRenderable(model.FormatHTML, overBudget()); err != nil {
		t.Errorf("an html body was refused: %v", err)
	}
}

// 被「槽位都占着」挡回去的那一次不算判断：它是对此刻的描述，不是对这份正文的描述。
//
// 记住它会把「忙的那一秒」变成一个长期显示为纯文本的页面，直到这条被逐出为止。下一次请求要再试。
func TestASaturatedRenderIsNotRemembered(t *testing.T) {
	cache := NewCache(DefaultCacheBytes)
	ordinary := "# Hello\n\nOrdinary prose."

	// 占满槽位：让两个跑不完的解析在后台。
	for i := 0; i < MaxConcurrentParses; i++ {
		go func() { _, _ = cache.Render(overBudget(), true) }()
	}
	deadline := time.Now().Add(2 * time.Second)
	for cache.Peak() < int64(MaxConcurrentParses) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if peak := cache.Peak(); peak < int64(MaxConcurrentParses) {
		t.Skipf("could not saturate the cache (peak %d)", peak)
	}

	got, within := cache.Render(ordinary, true)
	if within {
		t.Fatalf("a saturated render was judged as rendered: %q", got)
	}
	// 关键的一条：它没有被记住，所以槽位空出来之后同一份正文能正常渲染。
	if _, cached := cache.lookup(cacheKey(ordinary, true)); cached {
		t.Fatal("the saturated fallback was remembered: the page would stay text")
	}
}
