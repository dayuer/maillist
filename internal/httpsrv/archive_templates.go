package httpsrv

import (
	"encoding/json"
	"html/template"
	"net/http"

	"nibwire/internal/core/archive"
)

// The archive's pages are Go templates with the stylesheet inline: there is no front-end build in this
// port (design §4), and a reader's page has to render in one request from one binary.
//
// The markup is not a copy of upstream's: that is a React app, and design §13 asks this port to keep
// the archive, not to look identical. What is kept is what the pages are for — the issue's text, who
// wrote it, when it went out, how many read it, the way back to the others, and the way to subscribe.

var archiveTemplates = template.Must(template.New("archive").
	Funcs(template.FuncMap{"jsonld": jsonLD, "body": trustedBody}).
	Parse(archiveIndexTemplate + archiveIssueTemplate + archiveAuthorTemplate + archiveLayout))

// trustedBody is an issue's rendered body, which core/archive has already put through the sanitizer:
// that is the one place that decides what may run on an archive page, and running it through
// html/template's escaper afterwards would show the reader markup instead of an article.
//
// The frame path is not this: an iframe's srcdoc is an attribute, and there the escaper is exactly
// what is wanted.
func trustedBody(b archive.Body) template.HTML {
	return template.HTML(b.HTML) //nolint:gosec // G203: sanitized by core/archive.RenderIssueBody
}

// writeArchive renders one page.
func (s *Server) writeArchive(w http.ResponseWriter, page archivePage, name, cacheControl string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", cacheControl)
	if err := archiveTemplates.ExecuteTemplate(w, name, page); err != nil {
		// The status and the headers are already out, so the reader gets a truncated page; the log is
		// where an operator finds out why, and the request line beside it says which page.
		s.Log.Error("render the archive", "template", name, "error", err)
	}
}

// jsonLD is the structured data as a script tag: encoding/json does the escaping, which is what keeps
// an issue's subject from closing the tag.
func jsonLD(data map[string]any) template.JS {
	encoded, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	return template.JS(encoded) //nolint:gosec // G203: JSON-LD is JSON we built, marshalled here
}

const archiveLayout = `{{define "head"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.SEO.Metadata.Title}}</title>
<meta name="description" content="{{.SEO.Metadata.Description}}">
<link rel="canonical" href="{{.SEO.Metadata.Canonical}}">
<meta name="robots" content="{{if .SEO.Metadata.Robots.Index}}index{{else}}noindex{{end}},{{if .SEO.Metadata.Robots.Follow}}follow{{else}}nofollow{{end}}">
{{with .SEO.Metadata.Icon}}<link rel="icon" href="{{.}}">
{{end}}{{range .SEO.Metadata.Authors}}<meta name="author" content="{{.Name}}">
{{end}}<meta property="og:type" content="{{.SEO.Metadata.OpenGraph.Type}}">
<meta property="og:site_name" content="{{.SEO.Metadata.OpenGraph.SiteName}}">
<meta property="og:locale" content="{{.SEO.Metadata.OpenGraph.Locale}}">
<meta property="og:url" content="{{.SEO.Metadata.OpenGraph.URL}}">
<meta property="og:title" content="{{.SEO.Metadata.OpenGraph.Title}}">
<meta property="og:description" content="{{.SEO.Metadata.OpenGraph.Description}}">
{{with .SEO.Metadata.OpenGraph.PublishedTime}}<meta property="og:article:published_time" content="{{.}}">
{{end}}{{range .SEO.Metadata.OpenGraph.Images}}<meta property="og:image" content="{{.}}">
{{end}}{{range .SEO.Metadata.OpenGraph.Authors}}<meta property="og:article:author" content="{{.}}">
{{end}}<meta name="twitter:card" content="{{.SEO.Metadata.Twitter.Card}}">
<meta name="twitter:title" content="{{.SEO.Metadata.Twitter.Title}}">
<meta name="twitter:description" content="{{.SEO.Metadata.Twitter.Description}}">
{{with .SEO.Metadata.Twitter.Site}}<meta name="twitter:site" content="{{.}}">
{{end}}{{with .SEO.Metadata.Twitter.Creator}}<meta name="twitter:creator" content="{{.}}">
{{end}}{{range .SEO.Metadata.Twitter.Images}}<meta name="twitter:image" content="{{.}}">
{{end}}<script type="application/ld+json">{{jsonld .SEO.JSONLD}}</script>
<style>` + archiveStyles + `</style>
</head>
<body>{{end}}

{{define "header"}}<header class="site"><a href="{{.URLs.Index}}">{{.Newsletter.Name}}</a></header>{{end}}

{{define "pitch"}}<section class="pitch"><h2>Subscribe</h2>
<p>{{if .Newsletter.Description}}{{.Newsletter.Description}}{{else}}Get every issue by email.{{end}}</p>
<p>{{.Readers}} readers.</p>
<form method="post" action="/subscribe/{{.Newsletter.Slug}}">
<input type="email" name="email" placeholder="you@example.com" required aria-label="Email">
<button type="submit">Subscribe</button>
</form></section>{{end}}

{{define "cards"}}{{range .}}<li class="card"><a href="{{.URL}}">
{{if .Thumbnail}}<img src="{{.Thumbnail}}" alt="" loading="lazy">{{end}}
<h3>{{.Subject}}</h3>
{{if .PreviewText}}<p>{{.PreviewText}}</p>{{end}}
<p class="meta">{{.Date}}{{if gt .WebViews 0}} &middot; {{.WebViews}} views{{end}}</p>
</a></li>{{end}}{{end}}

{{define "footer"}}<footer class="site">
{{range .Newsletter.SocialLinks}}<a href="{{.}}">{{.}}</a> {{end}}
<p><a href="{{.URLs.Index}}">{{.Newsletter.Name}}</a>{{with .Newsletter.PostalAddress}} &middot; {{.}}{{end}}</p>
</footer></body></html>{{end}}`

const archiveIndexTemplate = `{{define "archive_index"}}{{template "head" .}}{{template "header" .}}
<main><h1>{{.Newsletter.Name}}</h1>
{{with .Newsletter.Description}}<p class="lead">{{.}}</p>{{end}}
{{if .Author.Bio}}<p class="byline">by <a href="{{.URLs.Author .Author.Slug}}">{{.Author.Name}}</a></p>{{end}}
{{if .Issues}}<ol class="cards">{{template "cards" .Issues}}</ol>
{{else}}<p>No issues yet.</p>{{end}}
{{template "pitch" .}}</main>
{{template "footer" .}}{{end}}`

const archiveIssueTemplate = `{{define "archive_issue"}}{{template "head" .}}{{template "header" .}}
<main><article>
<h1>{{.Issue.Card.Subject}}</h1>
{{if .Issue.Card.PreviewText}}<p class="lead">{{.Issue.Card.PreviewText}}</p>{{end}}
<p class="byline">by <a href="{{.URLs.Author .Author.Slug}}">{{.Author.Name}}</a>{{if .Issue.Card.Date}} &middot; {{.Issue.Card.Date}}{{end}}{{if gt .Issue.Card.WebViews 0}} &middot; {{.Issue.Card.WebViews}} views{{end}}</p>
{{if eq .Issue.Body.Kind "frame"}}
<iframe class="frame" title="Issue" sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox" srcdoc="{{.Issue.Body.HTML}}"></iframe>
{{else}}
<div class="prose">{{body .Issue.Body}}</div>
{{end}}
</article>
{{template "pitch" .}}
{{if .Issues}}<section class="more"><h2>Keep reading</h2><ol class="cards">{{template "cards" .Issues}}</ol></section>{{end}}
</main>
{{template "footer" .}}{{end}}`

const archiveAuthorTemplate = `{{define "archive_author"}}{{template "head" .}}{{template "header" .}}
<main><h1>{{.Author.Name}}</h1>
{{if .Author.ImageURL}}<img class="avatar" src="{{.Author.ImageURL}}" alt="{{.Author.Name}}">{{end}}
{{with .Author.Bio}}<p class="lead">{{.}}</p>{{end}}
{{if .Newsletter.SocialLinks}}<p class="meta">{{range .Newsletter.SocialLinks}}<a href="{{.}}">{{.}}</a> {{end}}</p>{{end}}
{{if .Issues}}<ol class="cards">{{template "cards" .Issues}}</ol>{{end}}
{{template "pitch" .}}</main>
{{template "footer" .}}{{end}}`

// archiveStyles is the whole stylesheet: a reading column, a card grid, and nothing that needs a
// build step. The prose rules are the ones a Markdown issue needs to look like an article rather than
// an unstyled document.
const archiveStyles = `
:root { color-scheme: light dark; --ink:#18181b; --muted:#71717a; --line:#e4e4e7; --card:#fafafa; }
* { box-sizing:border-box; }
body { margin:0; font:16px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif; color:var(--ink); }
a { color:inherit; }
header.site, footer.site { max-width:42rem; margin:0 auto; padding:1rem 1.25rem; }
header.site a { font-weight:600; text-decoration:none; }
footer.site { color:var(--muted); font-size:.85rem; border-top:1px solid var(--line); margin-top:3rem; }
footer.site a { margin-right:.75rem; }
main { max-width:42rem; margin:0 auto; padding:2rem 1.25rem 1rem; }
h1 { font-size:2rem; line-height:1.15; letter-spacing:-0.02em; margin:0 0 .5rem; }
.lead { font-size:1.15rem; color:var(--muted); margin-top:0; }
.byline, .meta { color:var(--muted); font-size:.9rem; }
.prose img { max-width:100%; height:auto; }
.prose table { width:100%; border-collapse:collapse; }
.prose th, .prose td { border:1px solid var(--line); padding:.4rem .6rem; text-align:left; }
.prose pre { overflow-x:auto; background:var(--card); padding:.75rem; border-radius:.5rem; }
.prose code { font-size:.9em; }
.prose blockquote { margin:1rem 0; padding-left:1rem; border-left:3px solid var(--line); color:var(--muted); }
.prose h2, .prose h3, .prose h4 { margin-top:2rem; line-height:1.25; }
.frame { width:100%; min-height:800px; border:1px solid var(--line); border-radius:.75rem; background:#fff; }
.avatar { width:4rem; height:4rem; border-radius:999px; object-fit:cover; }
ol.cards { list-style:none; padding:0; margin:2rem 0 0; display:grid; gap:1rem; }
ol.cards .card a { display:block; padding:1rem; border:1px solid var(--line); border-radius:.75rem; text-decoration:none; }
ol.cards .card img { width:100%; height:auto; border-radius:.5rem; }
ol.cards .card h3 { margin:.25rem 0; font-size:1.05rem; }
ol.cards .card p { margin:.25rem 0 0; color:var(--muted); font-size:.9rem; }
.pitch { margin-top:3rem; padding:1.5rem; border:1px solid var(--line); border-radius:1rem; background:var(--card); }
.pitch h2 { margin-top:0; font-size:1rem; text-transform:uppercase; letter-spacing:.05em; color:var(--muted); }
.pitch form { display:flex; gap:.5rem; flex-wrap:wrap; }
.pitch input { flex:1 1 12rem; padding:.6rem .75rem; border:1px solid var(--line); border-radius:.5rem; font:inherit; }
.pitch button { padding:.6rem 1rem; border:0; border-radius:.5rem; background:var(--ink); color:#fff; font:inherit; font-weight:600; cursor:pointer; }
.more h2 { font-size:.85rem; text-transform:uppercase; letter-spacing:.05em; color:var(--muted); border-bottom:1px solid var(--line); padding-bottom:.5rem; }
`
