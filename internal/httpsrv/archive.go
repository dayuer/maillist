package httpsrv

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"mailcheap/internal/core/archive"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/stats"
	"mailcheap/internal/store"
)

// The archive is the deployment's public side: a newsletter's past issues, the author's page, the
// sitemap and robots.txt (design §10). Two domains can serve it, and which one a request came in on
// decides the shape of its paths:
//
//	on the newsletter's own domain (ARCHIVE_HOSTS):   /        /p/{post}   /authors/{author}
//	                                                  /sitemap.xml  /robots.txt
//	on the application's:                             /archive/{slug}  /archive/{slug}/p/{post}
//	                                                  /archive/{slug}/authors/{author}
//	                                                  /p/{slug} redirects to the above
//
// The pages are rendered per request from SQLite (design §10): there is no ISR layer, which is also
// what makes the view count exact.

// archiveSlugPattern is ARCHIVE_SLUG_PATTERN as a path segment: lowercase letters, digits, dashes.
var archiveSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,119}$`)

// archiveCacheControl is what caddy may keep. The pages change when an issue is edited or a reader
// arrives, so a minute of shared caching is enough to absorb a spike and short enough not to matter.
const archiveCacheControl = "public, max-age=60, stale-while-revalidate=600"

// feedCacheControl is longer for the two machine-read files, which change only when an issue goes out.
const feedCacheControl = "public, max-age=3600"

// archiveIndexLimit is how many issues the index lists. The archive goes back as far as the
// newsletter does; a page nobody scrolls to the end of is still a page that has to be rendered.
const archiveIndexLimit = 100

// archiveMoreLimit is the "keep reading" strip on an issue page (upstream asks for three).
const archiveMoreLimit = 3

// archivePage is what a template is given.
type archivePage struct {
	Newsletter  model.Newsletter
	Author      archive.Author
	URLs        archive.URLs
	SEO         archive.SEO
	Issues      []issueCard
	Issue       *issueView
	Readers     int
	ArchiveHost string
	Path        string // the current page, for the canonical link and the subscribe form
}

type issueCard struct {
	Slug        string
	Subject     string
	PreviewText string
	Thumbnail   string
	Date        string
	WebViews    int
	Pinned      bool
	URL         string
}

type issueView struct {
	Card issueCard
	Body archive.Body
}

// serveArchive routes the archive paths. It is registered by routes(); every handler resolves which
// newsletter it is about and refuses when there is none.
func (s *Server) serveArchive(w http.ResponseWriter, r *http.Request) {
	hostSlug, onArchiveHost := ArchiveSlug(r.Context())
	slug := r.PathValue("slug")
	if onArchiveHost {
		// The newsletter is the domain's: an /archive/... path on it is a link to the application
		// domain, not a page.
		if slug != "" {
			http.NotFound(w, r)
			return
		}
		slug = hostSlug
	} else if slug == "" {
		// The root of the application's domain is not an archive.
		http.NotFound(w, r)
		return
	}
	if !archiveSlugPattern.MatchString(slug) {
		http.NotFound(w, r)
		return
	}

	newsletter, err := s.Store.NewsletterBySlug(r.Context(), slug)
	if err != nil || !newsletter.ArchiveEnabled {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.apiFailure(w, "read the newsletter", err)
			return
		}
		http.NotFound(w, r)
		return
	}
	s.renderArchive(w, r, newsletter)
}

// renderArchive dispatches on the path once the newsletter is known.
func (s *Server) renderArchive(w http.ResponseWriter, r *http.Request, n model.Newsletter) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/sitemap.xml"):
		s.archiveSitemap(w, r, n)
	case strings.HasSuffix(r.URL.Path, "/robots.txt"):
		s.archiveRobots(w, r, n)
	case r.PathValue("post") != "":
		s.archiveIssue(w, r, n, r.PathValue("post"))
	case r.PathValue("author") != "":
		s.archiveAuthor(w, r, n, r.PathValue("author"))
	default:
		s.archiveIndex(w, r, n)
	}
}

// shortIssuePath is the application domain's /p/{slug}: it does not name the newsletter, so it
// resolves the slug to one and sends the reader to the archive's own address for it (307, upstream's
// choice: a permanent redirect would pin a slug that an operator can change).
func (s *Server) shortIssuePath(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !archiveSlugPattern.MatchString(slug) {
		http.NotFound(w, r)
		return
	}
	if hostSlug, onArchiveHost := ArchiveSlug(r.Context()); onArchiveHost {
		// On the newsletter's own domain this path is the issue's page itself.
		newsletter, err := s.Store.NewsletterBySlug(r.Context(), hostSlug)
		if err != nil || !newsletter.ArchiveEnabled {
			http.NotFound(w, r)
			return
		}
		s.archiveIssue(w, r, newsletter, slug)
		return
	}
	newsletter, err := s.Store.NewsletterForArchiveSlug(r.Context(), slug)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.apiFailure(w, "read the issue", err)
			return
		}
		http.NotFound(w, r)
		return
	}
	target := archive.ForNewsletter(newsletter.Slug, s.Config.ArchiveHosts, s.Config.AppURL).Post(slug)
	// The address is the archive's own: its host comes from ARCHIVE_HOSTS and its path from the slug
	// of an issue that is in this database, checked against archiveSlugPattern just above. Nothing in
	// it comes from the request.
	http.Redirect(w, r, target, http.StatusTemporaryRedirect) //nolint:gosec // G710: not a request-supplied URL
}

// archiveIndex is the newsletter's own front page: the issues, newest first.
func (s *Server) archiveIndex(w http.ResponseWriter, r *http.Request, n model.Newsletter) {
	issues, err := s.Store.PublishedIssues(r.Context(), n.ID)
	if err != nil {
		s.apiFailure(w, "read the issues", err)
		return
	}
	urls := archive.ForNewsletter(n.Slug, s.Config.ArchiveHosts, s.Config.AppURL)
	readers, err := s.Store.CountActiveContacts(r.Context(), n.ID)
	if err != nil {
		s.apiFailure(w, "count the readers", err)
		return
	}
	page := archivePage{
		Newsletter: n, Author: archive.AuthorOf(n), URLs: urls,
		SEO:     archive.HomeSEO(n, s.Config.AppURL, s.Config.ArchiveHosts),
		Issues:  s.cardList(issues, urls, archiveIndexLimit),
		Readers: readers,
	}
	s.writeArchive(w, page, "archive_index", archiveCacheControl)
}

// archiveIssue is one issue's page. The view is counted before it is rendered, so the number on the
// page includes this reader (upstream counted the same way, one beacon later).
func (s *Server) archiveIssue(w http.ResponseWriter, r *http.Request, n model.Newsletter, slug string) {
	if !archiveSlugPattern.MatchString(slug) {
		http.NotFound(w, r)
		return
	}
	issue, err := s.Store.CampaignByArchiveSlug(r.Context(), n.ID, slug)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.apiFailure(w, "read the issue", err)
			return
		}
		http.NotFound(w, r)
		return
	}
	if counted := s.countView(r, issue.ID); counted {
		issue.WebViews++
	}

	all, err := s.Store.PublishedIssues(r.Context(), n.ID)
	if err != nil {
		s.apiFailure(w, "read the issues", err)
		return
	}
	urls := archive.ForNewsletter(n.Slug, s.Config.ArchiveHosts, s.Config.AppURL)
	view := issueView{Card: cardOf(issue, urls), Body: archive.RenderIssueBody(issue, n)}
	page := archivePage{
		Newsletter: n, Author: archive.AuthorOf(n), URLs: urls,
		SEO:    archive.IssueSEO(n, issue, s.Config.AppURL, s.Config.ArchiveHosts),
		Issue:  &view,
		Issues: s.otherIssues(all, urls, issue.ID, archiveMoreLimit),
		Path:   r.URL.Path,
	}
	s.writeArchive(w, page, "archive_issue", archiveCacheControl)
}

// archiveAuthor is the author's page: the one person who writes the newsletter, with the profiles
// that are theirs.
func (s *Server) archiveAuthor(w http.ResponseWriter, r *http.Request, n model.Newsletter, slug string) {
	author := archive.AuthorOf(n)
	if slug != author.Slug {
		http.NotFound(w, r)
		return
	}
	issues, err := s.Store.PublishedIssues(r.Context(), n.ID)
	if err != nil {
		s.apiFailure(w, "read the issues", err)
		return
	}
	urls := archive.ForNewsletter(n.Slug, s.Config.ArchiveHosts, s.Config.AppURL)
	page := archivePage{
		Newsletter: n, Author: author, URLs: urls,
		SEO:    archive.AuthorSEO(n, s.Config.AppURL, s.Config.ArchiveHosts),
		Issues: s.cardList(issues, urls, archiveIndexLimit),
		Path:   r.URL.Path,
	}
	s.writeArchive(w, page, "archive_author", archiveCacheControl)
}

// archiveSitemap is the XML search engines read: the index, the author page and every issue.
func (s *Server) archiveSitemap(w http.ResponseWriter, r *http.Request, n model.Newsletter) {
	issues, err := s.Store.PublishedIssues(r.Context(), n.ID)
	if err != nil {
		s.apiFailure(w, "read the issues", err)
		return
	}
	urls := archive.ForNewsletter(n.Slug, s.Config.ArchiveHosts, s.Config.AppURL)
	author := archive.AuthorOf(n)

	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")
	latest := ""
	if len(issues) > 0 {
		if date := archive.IssueDate(issues[0]); date != nil {
			latest = date.UTC().Format(time.RFC3339)
		}
	}
	writeXMLEntry(&b, urls.SiteIndex, latest)
	writeXMLEntry(&b, urls.AbsoluteAuthor(author.Slug), latest)
	for _, issue := range issues {
		date := ""
		if at := archive.IssueDate(issue); at != nil {
			date = at.UTC().Format(time.RFC3339)
		}
		writeXMLEntry(&b, urls.AbsolutePost(issue.ArchiveSlug), date)
	}
	b.WriteString("</urlset>\n")

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", feedCacheControl)
	_, _ = w.Write([]byte(b.String()))
}

// archiveRobots points crawlers at the sitemap.
//
// The address has to be one this server answers: on the newsletter's own domain the file is served at
// /sitemap.xml, and under the application's it is at /archive/{slug}/sitemap.xml. Upstream's Next app
// served the second one at /archive/<slug>/sitemap (no extension, because that is what its route file
// was called) and this port names it .xml — which is the sort of difference that produces a robots.txt
// pointing at a 404. The test fetches what this writes.
func (s *Server) archiveRobots(w http.ResponseWriter, r *http.Request, n model.Newsletter) {
	urls := archive.ForNewsletter(n.Slug, s.Config.ArchiveHosts, s.Config.AppURL)
	sitemap := strings.TrimSuffix(urls.SiteIndex, "/") + "/sitemap.xml"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", feedCacheControl)
	_, _ = w.Write([]byte("User-agent: *\nAllow: /\n\nSitemap: " + sitemap + "\n"))
}

func writeXMLEntry(b *strings.Builder, loc, lastmod string) {
	b.WriteString("<url><loc>" + escapeXML(loc) + "</loc>")
	if lastmod != "" {
		b.WriteString("<lastmod>" + lastmod + "</lastmod>")
	}
	b.WriteString("</url>\n")
}

// escapeXML is the five characters that cannot appear raw in an XML text node. (&amp; has to come
// first, or it would escape the escapes.)
func escapeXML(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	return strings.ReplaceAll(value, "'", "&apos;")
}

// countView decides whether this request is a reader, and counts it if so.
//
// Two filters, both upstream's (app/api/views/route.js): a browser that looks like a bot, a crawler
// or a command-line tool doesn't count, and one address counts once a day for one issue. Upstream's
// second filter was the rate limiter; server-side rendering has no beacon to throttle, and a day is
// what "readers" means.
func (s *Server) countView(r *http.Request, campaignID string) bool {
	userAgent := r.Header.Get("User-Agent")
	if archive.IsBotUserAgent(userAgent) {
		return false
	}
	key := "views:" + campaignID + ":" + s.clientIP(r) + ":" + stats.DateKey(s.now(), time.UTC)
	if s.ArchiveViews.Limited(viewsOnceLimit, key, s.now()) {
		return false
	}
	if err := s.Store.AddWebViews(r.Context(), campaignID, 1); err != nil {
		// A missed view is not worth failing the page over.
		s.Log.Warn("could not count an archive view", "campaign", campaignID, "error", err)
		return false
	}
	return true
}

// viewsOnceLimit is the per-key limit the view counter uses: one. The limiter is a map with a
// window, which is exactly the "this key was seen" memory the deduplication needs.
var viewsOnceLimit = Limit{Name: "archive-view", Count: 1, Window: 24 * time.Hour}

// cardList renders the issues for a list, at most limit of them.
func (s *Server) cardList(issues []model.Campaign, urls archive.URLs, limit int) []issueCard {
	if len(issues) > limit {
		issues = issues[:limit]
	}
	out := make([]issueCard, 0, len(issues))
	for _, issue := range issues {
		out = append(out, cardOf(issue, urls))
	}
	return out
}

// otherIssues is the "keep reading" strip: everything but the issue being read.
func (s *Server) otherIssues(issues []model.Campaign, urls archive.URLs, skipID string, limit int) []issueCard {
	out := make([]issueCard, 0, limit)
	for _, issue := range issues {
		if issue.ID == skipID {
			continue
		}
		out = append(out, cardOf(issue, urls))
		if len(out) == limit {
			break
		}
	}
	return out
}

func cardOf(c model.Campaign, urls archive.URLs) issueCard {
	date := ""
	if at := archive.IssueDate(c); at != nil {
		date = at.UTC().Format("2 January 2006")
	}
	return issueCard{
		Slug: c.ArchiveSlug, Subject: c.Subject, PreviewText: c.PreviewText,
		Thumbnail: c.ThumbnailURL, Date: date, WebViews: c.WebViews,
		Pinned: c.ArchivePinned, URL: urls.Post(c.ArchiveSlug),
	}
}
