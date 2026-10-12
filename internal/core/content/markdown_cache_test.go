package content

import (
	"strings"
	"sync"
	"testing"
	"time"

	"mailcheap/internal/core/slowinput"
)

// overBudget is a body a CommonMark parser cannot finish inside the budget: a long run of link openers.
func overBudget() string { return slowinput.Repeat("[a](", 100_000) }

// 并发上限：无论多少请求同时撞上一份超预算的正文，同时在跑的解析不超过 MaxConcurrentParses。
//
// 这是「等待有界、CPU 无界」的修法：预算内的解析占槽位只有微秒，真正会长时间占住槽位的是被放弃的
// 那些；槽位满了就不再起新的 goroutine，直接给纯文本。
func TestAbandonedParsesAreCapped(t *testing.T) {
	cache := NewCache()
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
