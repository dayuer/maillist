package httpsrv

import (
	"encoding/base64"
	"html"
	"net/http"

	"nibwire/internal/core/sesmap"
)

// pixelGIF is upstream's PIXEL: a 1×1 transparent GIF, byte for byte.
var pixelGIF, _ = base64.StdEncoding.DecodeString("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7")

// handleOpen answers the tracking pixel. The image always loads — a reader must never see a broken
// one because a token was forged — and an open is recorded only for a token we signed.
//
// Upstream records after the response (Next's after()); here the write is one local SQLite
// statement, so it runs inline: sub-millisecond, deterministic in tests, and with no goroutine that
// could outlive a shutdown.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if messageID, ok := s.Signer.ReadOpenToken(r.PathValue("token")); ok {
		if _, err := s.Events.ApplyTracking(r.Context(), messageID, sesmap.EventOpen, "", s.clock()); err != nil {
			s.Log.Warn("open not recorded", "message", messageID, "error", err)
		}
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	_, _ = w.Write(pixelGIF)
}

// handleClick forwards a signed click and offers an unsigned one as a link to click. Redirecting an
// unsigned link automatically would let anyone send visitors anywhere through this domain.
func (s *Server) handleClick(w http.ResponseWriter, r *http.Request) {
	link, ok := s.Signer.ReadClickToken(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !link.Signed {
		writeContinuePage(w, link.URL)
		return
	}
	if _, err := s.Events.ApplyTracking(r.Context(), link.MessageID, sesmap.EventClick,
		r.UserAgent(), s.clock()); err != nil {
		s.Log.Warn("click not recorded", "message", link.MessageID, "error", err)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, link.URL, http.StatusFound)
}

// handleClickHead answers link checkers: same status, never counted, no body.
func (s *Server) handleClickHead(w http.ResponseWriter, r *http.Request) {
	link, ok := s.Signer.ReadClickToken(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if link.Signed {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Location", link.URL)
		w.WriteHeader(http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// writeContinuePage is upstream continuePage: the address, escaped, as a link the reader chooses.
func writeContinuePage(w http.ResponseWriter, target string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<meta name="robots" content="noindex"><title>Continue</title></head>` +
		`<body style="margin:0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;color:#27272a;">` +
		`<main style="max-width:32rem;margin:20vh auto;padding:0 20px;"><p>This link goes to:</p>` +
		`<p><a href="` + html.EscapeString(target) + `" rel="noopener noreferrer nofollow" ` +
		`style="color:#18181b;word-break:break-all;">` + html.EscapeString(target) + `</a></p></main></body></html>`))
}
