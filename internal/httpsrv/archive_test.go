package httpsrv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
)

// seedIssue publishes one issue in the seeded newsletter.
func (s *testServer) seedIssue(t *testing.T, newsletter model.Newsletter, subject, slug string) model.Campaign {
	t.Helper()
	sent := s.now.Add(-24 * time.Hour)
	campaign := model.NewCampaign(newsletter.ID, subject, sent)
	campaign.Status = model.CampaignSent
	campaign.ArchiveSlug = slug
	campaign.ArchiveListed = true
	campaign.SentAt = &sent
	campaign.Format = model.FormatMarkdown
	campaign.Body = "# Hello\n\nRead [this](https://example.com/read).\n\n![a](https://images.example.com/a.png)"
	if err := s.store.CreateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}
	return campaign
}

// 归档首页列出发过的每一期，issue 页给出正文，作者页给出作者。
func TestTheArchiveServesItsPages(t *testing.T) {
	s := newTestServer(t)
	newsletter := s.seedNewsletter(t)
	issue := s.seedIssue(t, newsletter, "Issue 12", "issue-12")

	index := s.get(t, "/archive/field-notes")
	if index.StatusCode != http.StatusOK {
		t.Fatalf("index = %d", index.StatusCode)
	}
	page := body(t, index)
	for _, want := range []string{
		"Field notes", "Issue 12", `href="/archive/field-notes/p/issue-12"`,
		`<link rel="canonical" href="https://mail.example.com/archive/field-notes">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("index is missing %q", want)
		}
	}

	post := s.get(t, "/archive/field-notes/p/issue-12")
	if post.StatusCode != http.StatusOK {
		t.Fatalf("issue = %d", post.StatusCode)
	}
	article := body(t, post)
	for _, want := range []string{
		"Issue 12", "<h1>Hello</h1>", `href="https://example.com/read"`, "Jane Doe",
		`href="/archive/field-notes/authors/jane-doe"`,
		`<link rel="canonical" href="https://mail.example.com/archive/field-notes/p/issue-12">`,
		`"@type":"BlogPosting"`,
	} {
		if !strings.Contains(article, want) {
			t.Errorf("issue page is missing %q", want)
		}
	}
	// 首图先加载。
	if !strings.Contains(article, `fetchpriority="high"`) {
		t.Errorf("no hero image on the issue page")
	}

	author := s.get(t, "/archive/field-notes/authors/jane-doe")
	if author.StatusCode != http.StatusOK || !strings.Contains(body(t, author), "Jane Doe") {
		t.Fatalf("author page = %d", author.StatusCode)
	}
	if other := s.get(t, "/archive/field-notes/authors/somebody-else"); other.StatusCode != http.StatusNotFound {
		t.Errorf("a stranger's author page = %d", other.StatusCode)
	}

	// 不存在的期、没开的归档、别的 newsletter 的期。
	if got := s.get(t, "/archive/field-notes/p/no-such-issue"); got.StatusCode != http.StatusNotFound {
		t.Errorf("unknown issue = %d", got.StatusCode)
	}
	if got := s.get(t, "/archive/no-such-newsletter"); got.StatusCode != http.StatusNotFound {
		t.Errorf("unknown newsletter = %d", got.StatusCode)
	}
	off := newsletter
	off.ID, off.Slug, off.ArchiveEnabled = ids.New(), "closed", false
	if err := s.store.CreateNewsletter(context.Background(), off); err != nil {
		t.Fatal(err)
	}
	if got := s.get(t, "/archive/closed"); got.StatusCode != http.StatusNotFound {
		t.Errorf("archive off = %d, want 404", got.StatusCode)
	}

	// 未列出的一期：有页面，但不在列表里。
	unlisted := s.seedIssue(t, newsletter, "Hidden", "hidden")
	unlisted.ArchiveListed = false
	if err := s.store.UpdateCampaign(context.Background(), unlisted); err != nil {
		t.Fatal(err)
	}
	if got := s.get(t, "/archive/field-notes/p/hidden"); got.StatusCode != http.StatusOK {
		t.Errorf("an unlisted issue's page = %d", got.StatusCode)
	}
	if listed := body(t, s.get(t, "/archive/field-notes")); strings.Contains(listed, "Hidden") {
		t.Errorf("an unlisted issue is listed")
	}
	_ = issue
}

// HTML 的 issue 进 sandboxed iframe，Markdown 的进 article。
func TestAnHTMLIssueGoesIntoTheFrame(t *testing.T) {
	s := newTestServer(t)
	newsletter := s.seedNewsletter(t)
	campaign := s.seedIssue(t, newsletter, "Designed", "designed")
	campaign.Format = model.FormatHTML
	campaign.Body = `<html><head></head><body><table><tr><td>Boxed</td></tr></table></body></html>`
	if err := s.store.UpdateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}

	page := body(t, s.get(t, "/archive/field-notes/p/designed"))
	if !strings.Contains(page, `<iframe class="frame"`) || !strings.Contains(page, `sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"`) {
		t.Fatalf("no sandboxed frame: %s", page)
	}
	// srcdoc 里的内容被转义成属性，所以页面上看到的引号是 &#34;。
	if !strings.Contains(page, "Boxed") {
		t.Errorf("the issue's own markup is missing")
	}
	if strings.Contains(page, `<div class="prose">`) {
		t.Errorf("an HTML issue went into the article layout")
	}
}

// sitemap 与 robots：搜索引擎读的那两个文件。
func TestTheSitemapAndRobots(t *testing.T) {
	s := newTestServer(t)
	newsletter := s.seedNewsletter(t)
	s.seedIssue(t, newsletter, "Issue 12", "issue-12")

	sitemap := s.get(t, "/archive/field-notes/sitemap.xml")
	if sitemap.StatusCode != http.StatusOK ||
		!strings.HasPrefix(sitemap.Header.Get("Content-Type"), "application/xml") {
		t.Fatalf("sitemap = %d %s", sitemap.StatusCode, sitemap.Header.Get("Content-Type"))
	}
	xml := body(t, sitemap)
	for _, want := range []string{
		`<loc>https://mail.example.com/archive/field-notes</loc>`,
		`<loc>https://mail.example.com/archive/field-notes/authors/jane-doe</loc>`,
		`<loc>https://mail.example.com/archive/field-notes/p/issue-12</loc>`,
		"<lastmod>2026-10-11T12:00:00Z</lastmod>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("sitemap is missing %q:\n%s", want, xml)
		}
	}

	robots := s.get(t, "/archive/field-notes/robots.txt")
	if robots.StatusCode != http.StatusOK || !strings.HasPrefix(robots.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("robots = %d %s", robots.StatusCode, robots.Header.Get("Content-Type"))
	}
	text := body(t, robots)
	if !strings.Contains(text, "User-agent: *") {
		t.Errorf("robots = %q", text)
	}
	// 机器人文件里的每一个 Sitemap 地址都必须真的能取到：一条指向 404 的 Sitemap 就是搜索引擎读不到
	// 归档的全部原因，而这种错只有把那个地址真的取一次才发现得了（第二轮审计就是这样报出来的）。
	assertSitemaps(t, s, text, "")
}

// assertSitemaps fetches every Sitemap line of a robots.txt from this server, on the host that serves
// it, and requires a 200 that is really a sitemap.
func assertSitemaps(t *testing.T, s *testServer, robots, host string) {
	t.Helper()
	found := 0
	for _, line := range strings.Split(robots, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "Sitemap:")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		found++
		parsed, err := url.Parse(value)
		if err != nil {
			t.Errorf("robots names a Sitemap that is not a URL: %q", value)
			continue
		}
		response := s.getOnHost(t, parsed.RequestURI(), host)
		if response.StatusCode != http.StatusOK {
			t.Errorf("robots names %s, which answers %d", value, response.StatusCode)
			continue
		}
		if got := body(t, response); !strings.HasPrefix(got, "<?xml") {
			t.Errorf("%s did not answer a sitemap: %.40q", value, got)
		}
	}
	if found == 0 {
		t.Errorf("robots names no sitemap: %q", robots)
	}
}

// 归档域名上，路径换成那个域名自己的形状；应用域名上的 /p/{slug} 是 307。
func TestArchiveHostPathsAndTheShortLink(t *testing.T) {
	s := newTestServerWithArchiveHost(t, "news.example.com", "field-notes")
	newsletter := s.seedNewsletter(t)
	s.seedIssue(t, newsletter, "Issue 12", "issue-12")

	// 归档域名：首页、期、作者、sitemap、robots 都在根上。
	for path, want := range map[string]string{
		"/":                 "Field notes",
		"/p/issue-12":       "Issue 12",
		"/authors/jane-doe": "Jane Doe",
		"/sitemap.xml":      "https://news.example.com/p/issue-12",
		"/robots.txt":       "Sitemap: https://news.example.com/sitemap.xml",
	} {
		response := s.getOnHost(t, path, "news.example.com")
		if response.StatusCode != http.StatusOK {
			t.Errorf("%s on the archive host = %d", path, response.StatusCode)
			continue
		}
		if got := body(t, response); !strings.Contains(got, want) {
			t.Errorf("%s is missing %q", path, want)
		}
	}

	// 归档域名上的 robots 指的 sitemap 也必须取得到。
	assertSitemaps(t, s, body(t, s.getOnHost(t, "/robots.txt", "news.example.com")), "news.example.com")

	// 归档域名只服务读者的路径：管理面仍然 404。
	if got := s.getOnHost(t, "/healthz", "news.example.com"); got.StatusCode != http.StatusOK {
		t.Errorf("healthz on the archive host = %d", got.StatusCode)
	}
	for _, path := range []string{"/api/v1/contacts", "/mcp", "/archive/field-notes"} {
		if got := s.getOnHost(t, path, "news.example.com"); got.StatusCode != http.StatusNotFound {
			t.Errorf("%s on the archive host = %d, want 404", path, got.StatusCode)
		}
	}

	// 应用域名上的 /p/{slug}：有归档域名就跳过去。
	redirect := s.get(t, "/p/issue-12")
	if redirect.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("short link = %d", redirect.StatusCode)
	}
	if location := redirect.Header.Get("Location"); location != "https://news.example.com/p/issue-12" {
		t.Errorf("Location = %q", location)
	}

	// 没有归档域名的 newsletter 跳到自己域名下的归档路径。
	other := newTestServer(t)
	otherNewsletter := other.seedNewsletter(t)
	other.seedIssue(t, otherNewsletter, "Issue 12", "issue-12")
	plain := other.get(t, "/p/issue-12")
	// 相对地址：上游也是把归档页的路径直接放进 Location（浏览器按请求的域名解析）。
	if plain.StatusCode != http.StatusTemporaryRedirect ||
		plain.Header.Get("Location") != "/archive/field-notes/p/issue-12" {
		t.Errorf("short link without an archive host = %d %q", plain.StatusCode, plain.Header.Get("Location"))
	}
	if missing := other.get(t, "/p/no-such-issue"); missing.StatusCode != http.StatusNotFound {
		t.Errorf("unknown short link = %d", missing.StatusCode)
	}
}

// 阅读计数：服务端数，一天一个地址一次，机器不算。
func TestTheViewCountCountsReadersOnly(t *testing.T) {
	s := newTestServer(t)
	newsletter := s.seedNewsletter(t)
	issue := s.seedIssue(t, newsletter, "Issue 12", "issue-12")

	views := func() int {
		t.Helper()
		loaded, err := s.store.Campaign(context.Background(), issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		return loaded.WebViews
	}

	first := s.get(t, "/archive/field-notes/p/issue-12")
	if views() != 1 {
		t.Fatalf("views = %d after one visit", views())
	}
	// 页面上显示的是这次访问算进去之后的数。
	if !strings.Contains(body(t, first), "1 views") {
		t.Errorf("the page does not show the view it just counted")
	}

	// 同一个地址同一天再看：不重复计。
	s.get(t, "/archive/field-notes/p/issue-12")
	if views() != 1 {
		t.Fatalf("views = %d after a second visit from the same address", views())
	}

	// 机器不算：curl 的 UA 就在上游的名单里，空 UA 也算。
	for _, userAgent := range []string{"curl/8.4.0", "", "Mozilla/5.0 (compatible; Googlebot/2.1)"} {
		s.getAs(t, "/archive/field-notes/p/issue-12", userAgent, "198.51.100.7:1234")
		if views() != 1 {
			t.Fatalf("a machine (%q) was counted: %d", userAgent, views())
		}
	}

	// 另一个地址算一次。
	s.getAs(t, "/archive/field-notes/p/issue-12", browserUA, "203.0.113.9:1234")
	if views() != 2 {
		t.Fatalf("views = %d after a visit from somewhere else", views())
	}

	// sitemap 与首页不计。
	s.get(t, "/archive/field-notes")
	s.get(t, "/archive/field-notes/sitemap.xml")
	if views() != 2 {
		t.Fatalf("views = %d after list and sitemap requests", views())
	}
}

// 归档页的缓存头交给 caddy；页面本身不缓存任何私人的东西。
func TestArchiveCacheControl(t *testing.T) {
	s := newTestServer(t)
	newsletter := s.seedNewsletter(t)
	s.seedIssue(t, newsletter, "Issue 12", "issue-12")

	if got := s.get(t, "/archive/field-notes").Header.Get("Cache-Control"); !strings.Contains(got, "public") {
		t.Errorf("index cache-control = %q", got)
	}
	if got := s.get(t, "/archive/field-notes/sitemap.xml").Header.Get("Cache-Control"); !strings.Contains(got, "3600") {
		t.Errorf("sitemap cache-control = %q", got)
	}
	if got := s.get(t, "/archive/field-notes").Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("content-type = %q", got)
	}
}

// browserUA is the User-Agent the reader tests send: a person's, not a tool's.
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/141.0 Safari/537.36"

// getAs is one reader's request with the two things the view counter looks at: the User-Agent and the
// address it came from.
func (s *testServer) getAs(t *testing.T, path, userAgent, remoteAddr string) *http.Response {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("User-Agent", userAgent)
	request.RemoteAddr = remoteAddr
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, request)
	return recorder.Result()
}
