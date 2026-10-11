package httpsrv

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates/*.html
var templateFS embed.FS

// templates are parsed once at startup. They contain no JavaScript, which is why the CSP needs no
// script-src (see middleware.go).
var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// page is what every reader template renders: a title, the copy, and optionally a form or a link.
type page struct {
	Title       string
	Heading     string
	Description string
	// Action is where the form posts; an empty Action makes the page a notice.
	Action string
	Hidden map[string]string
	Button string
	// Link is an optional secondary link, such as the archive.
	Link      string
	LinkLabel string
}

// renderPage writes one reader page. The header goes out before the template runs, so a template
// error is a panic the tests will see rather than a half-written page in production.
func renderPage(w http.ResponseWriter, status int, name string, data page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		panic(err)
	}
}

// notice is the shared "this link doesn't work" page.
func notice(w http.ResponseWriter, status int, title, description string) {
	renderPage(w, status, "notice.html", page{Title: title, Heading: title, Description: description})
}
