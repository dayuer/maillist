package httpsrv

import (
	"context"
	"net/http"
	"strings"
)

// securityHeaders is upstream's next.config.mjs, value for value. There is deliberately no
// script-src: the reader templates ship no JavaScript, so this policy is both stricter and simpler
// than a per-response nonce would be — the same trade upstream explains for its cached archive pages.
func securityHeaders(header http.Header) {
	header.Set("Content-Security-Policy",
		"frame-ancestors 'none'; object-src 'none'; base-uri 'self'; form-action 'self'; script-src-attr 'none'")
	header.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
}

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w.Header())
		next.ServeHTTP(w, r)
	})
}

// readerOnlyPrefixes are the paths an archive host still serves. Everything else — the admin
// surface, the SES webhook — belongs to the app domain: an archive domain is public and must never
// reach it (design §10).
var readerOnlyPrefixes = []string{
	"/o/", "/c/", "/unsubscribe", "/api/unsubscribe", "/confirm", "/keep", "/subscribe/", "/healthz",
}

// archiveSlug maps an ARCHIVE_HOSTS entry's host to the newsletter slug it serves.
func (s *Server) archiveSlug(host string) (string, bool) {
	hostname := strings.ToLower(host)
	if i := strings.IndexByte(hostname, ':'); i >= 0 {
		hostname = hostname[:i]
	}
	if hostname == "" {
		return "", false
	}
	for _, pair := range strings.Split(s.Config.ArchiveHosts, ",") {
		host, slug, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.EqualFold(strings.TrimSpace(host), hostname) && strings.TrimSpace(slug) != "" {
			return strings.ToLower(strings.TrimSpace(slug)), true
		}
	}
	return "", false
}

func (s *Server) withArchiveHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slug, isArchive := s.archiveSlug(r.Host); isArchive {
			allowed := false
			for _, prefix := range readerOnlyPrefixes {
				if strings.HasPrefix(r.URL.Path, prefix) {
					allowed = true
					break
				}
			}
			if !allowed {
				http.NotFound(w, r)
				return
			}
			// The archive pages themselves arrive in phase 7; the slug is on the context for them.
			r = r.WithContext(withArchiveSlug(r.Context(), slug))
		}
		next.ServeHTTP(w, r)
	})
}

type archiveSlugKey struct{}

func withArchiveSlug(ctx context.Context, slug string) context.Context {
	return context.WithValue(ctx, archiveSlugKey{}, slug)
}

// ArchiveSlug is the newsletter an archive host serves, when the request arrived on one.
func ArchiveSlug(ctx context.Context) (string, bool) {
	slug, ok := ctx.Value(archiveSlugKey{}).(string)
	return slug, ok
}

// withRequestLog writes one line per request at debug level: the reader surface is public and
// chatty, so it is not worth a line each at info.
func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.clock()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.Log.Debug("http", "method", r.Method, "path", r.URL.Path,
			"status", recorder.status, "elapsed", s.clock().Sub(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if !w.wrote {
		w.status, w.wrote = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(body)
}
