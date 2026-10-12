package archive

import (
	"strings"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/slug"
)

// The archive's metadata and structured data (upstream lib/archive-seo.js). Two differences from
// upstream, both from design §13 cutting the share card:
//
//   - there is no /og route, so an issue without a picture has no share image rather than a generated
//     card. The meta tags simply leave it out.
//   - nothing here is a Next.js `Metadata` object: it is a struct the template reads, with the same
//     facts in it.
type SEO struct {
	Metadata Metadata
	JSONLD   map[string]any
}

// Metadata is what goes in the document head.
type Metadata struct {
	Canonical   string
	Title       string
	Description string
	Robots      Robots
	Icon        string
	Authors     []AuthorLink
	OpenGraph   OpenGraph
	Twitter     Twitter
}

// Robots is what search engines may do. An archive wants to be read: index and follow, always.
type Robots struct {
	Index  bool `json:"index"`
	Follow bool `json:"follow"`
}

// AuthorLink is one <meta name="author">.
type AuthorLink struct {
	Name string
	URL  string
}

type OpenGraph struct {
	Type          string
	SiteName      string
	Locale        string
	URL           string
	Title         string
	Description   string
	PublishedTime string
	Authors       []string
	Images        []string
}

type Twitter struct {
	Card        string
	Title       string
	Description string
	Site        string
	Creator     string
	Images      []string
}

// locale is upstream's hard-coded en_US, which is what the templates and the copy are written in.
const locale = "en_US"

// IssueSEO is upstream getIssueSEO: one issue's page.
func IssueSEO(n model.Newsletter, c model.Campaign, appURL, hosts string) SEO {
	urls := ForNewsletter(n.Slug, hosts, appURL)
	canonical := urls.AbsolutePost(issueSlugOf(c))
	description := c.PreviewText
	if description == "" {
		description = n.Description
	}
	author := AuthorOf(n)
	authorURL := urls.AbsoluteAuthor(author.Slug)
	image := shareImage(c, appURL)
	// Sent issues don't change; updatedAt moves with stats and pins, not with the content.
	published := ""
	if date := IssueDate(c); date != nil {
		published = date.UTC().Format(time.RFC3339)
	}
	handle := XHandle(n.SocialLinks)

	return SEO{
		Metadata: Metadata{
			Canonical:   canonical,
			Title:       c.Subject,
			Description: description,
			Robots:      Robots{Index: true, Follow: true},
			Icon:        n.ImageURL,
			Authors:     []AuthorLink{{Name: author.Name, URL: authorURL}},
			OpenGraph: OpenGraph{
				Type: "article", SiteName: n.Name, Locale: locale, URL: canonical,
				Title: c.Subject, Description: description, PublishedTime: published,
				Authors: []string{authorURL}, Images: oneIfSet(image),
			},
			Twitter: Twitter{
				Card: "summary_large_image", Title: c.Subject, Description: description,
				Site: handle, Creator: handle, Images: oneIfSet(image),
			},
		},
		JSONLD: map[string]any{
			"@context":         "https://schema.org",
			"@type":            "BlogPosting",
			"headline":         c.Subject,
			"description":      description,
			"url":              canonical,
			"mainEntityOfPage": map[string]any{"@type": "WebPage", "@id": canonical},
			"image":            oneIfSet(image),
			"datePublished":    published,
			"dateModified":     published,
			"author":           person(n, author, authorURL, appURL),
			"publisher":        publisher(n, appURL),
			"isPartOf": map[string]any{
				"@type": "Blog", "name": n.Name, "url": urls.SiteIndex,
			},
		},
	}
}

// HomeSEO is upstream getHomeSEO: the archive's own index.
func HomeSEO(n model.Newsletter, appURL, hosts string) SEO {
	urls := ForNewsletter(n.Slug, hosts, appURL)
	author := AuthorOf(n)
	authorURL := urls.AbsoluteAuthor(author.Slug)
	image := shareImage(model.Campaign{}, appURL)
	handle := XHandle(n.SocialLinks)

	return SEO{
		Metadata: Metadata{
			Canonical:   urls.SiteIndex,
			Title:       n.Name,
			Description: n.Description,
			Robots:      Robots{Index: true, Follow: true},
			Icon:        n.ImageURL,
			Authors:     []AuthorLink{{Name: author.Name, URL: authorURL}},
			OpenGraph: OpenGraph{
				Type: "website", SiteName: n.Name, Locale: locale, URL: urls.SiteIndex,
				Title: n.Name, Description: n.Description, Images: oneIfSet(image),
			},
			Twitter: Twitter{
				Card: "summary_large_image", Title: n.Name, Description: n.Description,
				Site: handle, Creator: handle, Images: oneIfSet(image),
			},
		},
		JSONLD: map[string]any{
			"@context":    "https://schema.org",
			"@type":       "Blog",
			"name":        n.Name,
			"description": n.Description,
			"url":         urls.SiteIndex,
			"image":       oneIfSet(absolute(n.ImageURL, appURL)),
			"author":      person(n, author, authorURL, appURL),
		},
	}
}

// AuthorSEO is upstream getAuthorSEO: the author's page, described as a profile.
func AuthorSEO(n model.Newsletter, appURL, hosts string) SEO {
	urls := ForNewsletter(n.Slug, hosts, appURL)
	author := AuthorOf(n)
	authorURL := urls.AbsoluteAuthor(author.Slug)
	description := author.Bio
	if description == "" {
		description = n.Description
	}
	image := absolute(author.ImageURL, appURL)
	if image == "" {
		image = shareImage(model.Campaign{}, appURL)
	}
	handle := XHandle(n.SocialLinks)
	// The photo shares as a square card; without one, the archive's card stands in.
	card := "summary_large_image"
	if author.ImageURL != "" {
		card = "summary"
	}

	return SEO{
		Metadata: Metadata{
			Canonical:   authorURL,
			Title:       author.Name,
			Description: description,
			Robots:      Robots{Index: true, Follow: true},
			Icon:        n.ImageURL,
			Authors:     []AuthorLink{{Name: author.Name, URL: authorURL}},
			OpenGraph: OpenGraph{
				Type: "profile", SiteName: n.Name, Locale: locale, URL: authorURL,
				Title: author.Name, Description: description, Images: oneIfSet(image),
			},
			Twitter: Twitter{
				Card: card, Title: author.Name, Description: description,
				Site: handle, Creator: handle, Images: oneIfSet(image),
			},
		},
		JSONLD: map[string]any{
			"@context":   "https://schema.org",
			"@type":      "ProfilePage",
			"url":        authorURL,
			"mainEntity": person(n, author, authorURL, appURL),
		},
	}
}

// person is upstream getPerson: search engines tie every issue to one person, whose page lists the
// profiles that are theirs.
func person(n model.Newsletter, author Author, authorURL, appURL string) map[string]any {
	out := map[string]any{
		"@type":  "Person",
		"name":   author.Name,
		"url":    authorURL,
		"sameAs": n.SocialLinks,
	}
	if image := absolute(author.ImageURL, appURL); image != "" {
		out["image"] = image
	}
	if author.Bio != "" {
		out["description"] = author.Bio
	}
	return out
}

func publisher(n model.Newsletter, appURL string) map[string]any {
	out := map[string]any{"@type": "Organization", "name": n.Name}
	if logo := absolute(n.ImageURL, appURL); logo != "" {
		out["logo"] = map[string]any{"@type": "ImageObject", "url": logo}
	}
	return out
}

// shareImage is what a link preview shows: the issue's own picture, and nothing when it has none.
//
// Upstream falls back to a generated card at /archive/<slug>/og. That route is the one feature design
// §13 cuts, so there is no fallback here — a page without a picture shares without one.
func shareImage(c model.Campaign, appURL string) string {
	return absolute(c.ThumbnailURL, appURL)
}

// absolute is upstream absoluteUrl: a path becomes an address on the deployment; an address stays.
func absolute(link, appURL string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	if strings.HasPrefix(link, "/") {
		return strings.TrimSuffix(appURL, "/") + link
	}
	return link
}

// oneIfSet is the list a meta tag wants: one entry, or none at all.
func oneIfSet(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

// issueSlugOf is the slug an issue's page lives at: the stored one, or the one its subject gives
// while it is still a draft — the same rule the email's Read-online link follows (upstream slugify).
func issueSlugOf(c model.Campaign) string {
	if c.ArchiveSlug != "" {
		return c.ArchiveSlug
	}
	return slug.Slug(c.Subject, 80)
}
