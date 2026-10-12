package archive

import (
	"testing"
	"time"

	"nibwire/internal/core/model"
)

// 夹具与上游 tests/archive-seo.test.mjs 的相同，好让断言能逐条对照。
var seoNewsletter = model.Newsletter{
	Name: "Field notes", Slug: "field-notes", FromName: "Jane Doe",
	ImageURL:    "/logos/field-notes.webp",
	Description: "One idea about building things, every Sunday.",
	SocialLinks: []string{"https://x.com/example", "https://youtube.com/@example"},
}

var seoIssue = model.Campaign{
	ID: "64f1a2b3c4d5e6f7a8b9c0d1", Subject: "How to price a side project",
	PreviewText: "Charge more than feels right.", ArchiveSlug: "how-to-price-a-side-project",
	ThumbnailURL: "https://images.example.com/thumb.png",
	Status:       model.CampaignSent,
	SentAt:       ptrTime(time.Date(2023, 10, 21, 10, 0, 0, 0, time.UTC)),
	UpdatedAt:    time.Date(2026, 10, 5, 17, 0, 0, 0, time.UTC),
}

const seoApp = "https://nibwire.test"

func ptrTime(at time.Time) *time.Time { return &at }

// 有归档域名的 newsletter 把搜索引擎指向自己的域名。
func TestIssueSEOPointsAtTheArchiveDomain(t *testing.T) {
	hosts := "news.example.com=field-notes"
	seo := IssueSEO(seoNewsletter, seoIssue, seoApp, hosts)

	if got := seo.Metadata.Canonical; got != "https://news.example.com/p/how-to-price-a-side-project" {
		t.Errorf("canonical = %q", got)
	}
	if !seo.Metadata.Robots.Index || !seo.Metadata.Robots.Follow {
		t.Errorf("robots = %+v", seo.Metadata.Robots)
	}
	if seo.Metadata.Title != seoIssue.Subject || seo.Metadata.Description != "Charge more than feels right." {
		t.Errorf("title/description = %q / %q", seo.Metadata.Title, seo.Metadata.Description)
	}
	if seo.Metadata.Icon != "/logos/field-notes.webp" {
		t.Errorf("icon = %q", seo.Metadata.Icon)
	}
	og := seo.Metadata.OpenGraph
	if og.Type != "article" || og.URL != seo.Metadata.Canonical {
		t.Errorf("openGraph = %+v", og)
	}
	if og.PublishedTime != "2023-10-21T10:00:00Z" {
		t.Errorf("publishedTime = %q", og.PublishedTime)
	}
	if len(og.Images) != 1 || og.Images[0] != "https://images.example.com/thumb.png" {
		t.Errorf("images = %v", og.Images)
	}
	tw := seo.Metadata.Twitter
	if tw.Card != "summary_large_image" || tw.Creator != "@example" || tw.Site != "@example" {
		t.Errorf("twitter = %+v", tw)
	}
}

// 没有分享图的 issue 就不给图——本端口没有 /og 路由（设计 §13 砍掉了分享图）。
func TestShareImageFallsBackToNothing(t *testing.T) {
	withoutThumbnail := seoIssue
	withoutThumbnail.ThumbnailURL = ""

	seo := IssueSEO(seoNewsletter, withoutThumbnail, seoApp, "news.example.com=field-notes")
	if len(seo.Metadata.OpenGraph.Images) != 0 || len(seo.Metadata.Twitter.Images) != 0 {
		t.Fatalf("a page with no picture shares one anyway: %+v", seo.Metadata.Twitter)
	}

	// 相对路径的图变成部署上的地址；绝对地址原样保留。
	relative := withoutThumbnail
	relative.ThumbnailURL = "/email/x.png"
	if got := IssueSEO(seoNewsletter, relative, seoApp, "").Metadata.OpenGraph.Images[0]; got != seoApp+"/email/x.png" {
		t.Errorf("images = %q", got)
	}
}

// issue 把自己描述成一篇博文。
func TestIssueJSONLDIsABlogPosting(t *testing.T) {
	seo := IssueSEO(seoNewsletter, seoIssue, seoApp, "")
	if seo.JSONLD["@type"] != "BlogPosting" || seo.JSONLD["headline"] != seoIssue.Subject {
		t.Fatalf("jsonLd = %+v", seo.JSONLD)
	}
	// 统计与置顶会动 updatedAt；已发出的 issue 不该因此看起来被改过。
	if seo.JSONLD["datePublished"] != "2023-10-21T10:00:00Z" || seo.JSONLD["dateModified"] != "2023-10-21T10:00:00Z" {
		t.Errorf("dates = %v / %v", seo.JSONLD["datePublished"], seo.JSONLD["dateModified"])
	}
	author, ok := seo.JSONLD["author"].(map[string]any)
	if !ok {
		t.Fatalf("author = %T", seo.JSONLD["author"])
	}
	if author["name"] != "Jane Doe" ||
		author["url"] != seoApp+"/archive/field-notes/authors/jane-doe" ||
		author["image"] != seoApp+"/logos/field-notes.webp" {
		t.Errorf("author = %+v", author)
	}
	if sameAs, ok := author["sameAs"].([]string); !ok || len(sameAs) != 2 {
		t.Errorf("sameAs = %v", author["sameAs"])
	}
	publisher, _ := seo.JSONLD["publisher"].(map[string]any)
	logo, _ := publisher["logo"].(map[string]any)
	if logo["url"] != seoApp+"/logos/field-notes.webp" {
		t.Errorf("publisher = %+v", publisher)
	}
	isPartOf, _ := seo.JSONLD["isPartOf"].(map[string]any)
	if isPartOf["url"] != seoApp+"/archive/field-notes" {
		t.Errorf("isPartOf = %+v", isPartOf)
	}
}

// 作者页是一个 profile：照片、bio 与社交账号。
func TestAuthorSEOIsAProfile(t *testing.T) {
	withAuthor := seoNewsletter
	withAuthor.AuthorBio = "I write about building small software companies."
	withAuthor.AuthorImageURL = "/authors/jane.jpg"

	seo := AuthorSEO(withAuthor, seoApp, "news.example.com=field-notes")
	if seo.Metadata.Canonical != "https://news.example.com/authors/jane-doe" {
		t.Errorf("canonical = %q", seo.Metadata.Canonical)
	}
	if seo.Metadata.OpenGraph.Type != "profile" {
		t.Errorf("openGraph.type = %q", seo.Metadata.OpenGraph.Type)
	}
	if got := seo.Metadata.OpenGraph.Images; len(got) != 1 || got[0] != seoApp+"/authors/jane.jpg" {
		t.Errorf("images = %v", got)
	}
	// 有照片时是方形卡片。
	if seo.Metadata.Twitter.Card != "summary" {
		t.Errorf("card = %q", seo.Metadata.Twitter.Card)
	}
	if seo.JSONLD["@type"] != "ProfilePage" {
		t.Fatalf("jsonLd = %+v", seo.JSONLD)
	}
	mainEntity, _ := seo.JSONLD["mainEntity"].(map[string]any)
	if mainEntity["image"] != seoApp+"/authors/jane.jpg" ||
		mainEntity["description"] != withAuthor.AuthorBio {
		t.Errorf("mainEntity = %+v", mainEntity)
	}

	// 没有照片时退回 newsletter 的图（这里连 newsletter 的图也没有，于是没有图），
	// bio 空时用 newsletter 的描述。
	withoutPhoto := seoNewsletter
	withoutPhoto.ImageURL = ""
	never := AuthorSEO(withoutPhoto, seoApp, "")
	if len(never.Metadata.OpenGraph.Images) != 0 {
		t.Errorf("images = %v", never.Metadata.OpenGraph.Images)
	}
	if never.Metadata.Twitter.Card != "summary_large_image" {
		t.Errorf("card = %q, want the archive's wide card without a photo", never.Metadata.Twitter.Card)
	}
	if never.Metadata.Description != seoNewsletter.Description {
		t.Errorf("description = %q", never.Metadata.Description)
	}
	if never.Metadata.Twitter.Creator != "@example" {
		t.Errorf("creator = %q", never.Metadata.Twitter.Creator)
	}
}

// 归档首页的 canonical 是它自己域名的根，没有域名时是应用域名下的路径。
func TestHomeSEO(t *testing.T) {
	seo := HomeSEO(seoNewsletter, seoApp, "news.example.com=field-notes")
	if seo.Metadata.Canonical != "https://news.example.com/" {
		t.Errorf("canonical = %q", seo.Metadata.Canonical)
	}
	if seo.JSONLD["@type"] != "Blog" {
		t.Errorf("jsonLd = %+v", seo.JSONLD)
	}

	noLinks := seoNewsletter
	noLinks.SocialLinks = nil
	withoutDomain := HomeSEO(noLinks, seoApp, "")
	if withoutDomain.Metadata.Canonical != seoApp+"/archive/field-notes" {
		t.Errorf("canonical = %q", withoutDomain.Metadata.Canonical)
	}
	if withoutDomain.Metadata.Twitter.Creator != "" {
		t.Errorf("creator = %q", withoutDomain.Metadata.Twitter.Creator)
	}
}

// 社交链接按域名认平台；认不出的用主机名。
func TestPlatformOfRecognizesSites(t *testing.T) {
	for _, tc := range []struct{ link, id string }{
		{"https://twitter.com/example", "x"},
		{"https://www.linkedin.com/in/example/", "linkedin"},
		{"https://m.youtube.com/@example", "youtube"},
		{"https://www.example.com", "website"},
	} {
		platform, ok := PlatformOf(tc.link)
		if !ok || platform.ID != tc.id {
			t.Errorf("%s → %+v %v, want %s", tc.link, platform, ok, tc.id)
		}
	}
	if _, ok := PlatformOf("not a link"); ok {
		t.Error("a non-link was taken for a platform")
	}
}

// 归档域名上的路径映射（上游 getArchiveHostUrl）。
func TestHostURLMovesAnArchivePathToItsDomain(t *testing.T) {
	hosts := "news.example.com=weekly,letters.example.org=monthly"
	for _, tc := range []struct{ pathname, search, want string }{
		{"/archive/weekly", "", "https://news.example.com/"},
		{"/archive/weekly/", "", "https://news.example.com/"},
		{"/archive/weekly/p/my-issue", "?ref=x", "https://news.example.com/p/my-issue?ref=x"},
		{"/archive/monthly/authors/jane-doe", "", "https://letters.example.org/authors/jane-doe"},
		{"/archive/monthly/sitemap", "", "https://letters.example.org/sitemap.xml"},
		{"/archive/monthly/robots", "", "https://letters.example.org/robots.txt"},
	} {
		got, ok := HostURL(tc.pathname, tc.search, hosts)
		if !ok || got != tc.want {
			t.Errorf("%s → %q %v, want %q", tc.pathname, got, ok, tc.want)
		}
	}
	// 没有归档域名的 newsletter 留在应用域名上，其他路径根本不是归档路径。
	for _, pathname := range []string{"/archive/no-domain/p/x", "/campaigns"} {
		if got, ok := HostURL(pathname, "", hosts); ok {
			t.Errorf("%s → %q, want no archive host", pathname, got)
		}
	}
	if got, ok := HostURL("/archive/weekly/p/x", "", ""); ok {
		t.Errorf("with no ARCHIVE_HOSTS at all: %q", got)
	}
}
