package httpsrv

import (
	"net/http"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/sign"
)

// KeepMaxAge is how long a keep link stays usable; upstream allows 90 days, well past the 14 days
// before a silent contact goes dormant.
const KeepMaxAge = 90 * 24 * time.Hour

// keepLimit is upstream's PER_IP: people click from home and office networks, and a shared one still
// fits several readers.
var keepLimit = Limit{Name: "keep-ip", Count: 20, Window: 10 * time.Minute}

// handleKeepPage shows the "yes, keep sending it" page.
//
// Upstream's page auto-submits its form from the browser (React's useEffect) because link scanners
// open pages without submitting forms. This port ships no JavaScript — the CSP has no script-src —
// so the reader clicks the button. That is stricter, not weaker: a scanner that merely opens the
// page still changes nothing.
func (s *Server) handleKeepPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	reference, ok := s.Signer.VerifyContactToken(sign.KindKeep, token, KeepMaxAge, s.clock())
	if !ok {
		notice(w, http.StatusNotFound, "This link doesn't work",
			"It may be older than 90 days. Sign up again to keep getting the newsletter.")
		return
	}
	newsletter, err := s.Store.Newsletter(r.Context(), reference.NewsletterID)
	if err != nil {
		notice(w, http.StatusNotFound, "This link doesn't work",
			"It may be older than 90 days. Sign up again to keep getting the newsletter.")
		return
	}
	renderPage(w, http.StatusOK, "keep.html", page{
		Title: "Keep getting the newsletter", Heading: "Keep getting " + newsletter.Name + "?",
		Description: "One click and it keeps coming.",
		Action:      "/keep?token=" + token, Button: "Yes, keep sending it",
	})
}

// handleKeep is the button. It brings a dormant contact back; an unsubscribe, a bounce or a
// complaint stays as it is.
func (s *Server) handleKeep(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if s.Limiter.Limited(keepLimit, s.clientIP(r), s.clock()) {
		rateLimited(w, keepLimit)
		return
	}
	reference, ok := s.Signer.VerifyContactToken(sign.KindKeep, token, KeepMaxAge, s.clock())
	if !ok {
		notice(w, http.StatusNotFound, "This link doesn't work",
			"It may be older than 90 days. Sign up again to keep getting the newsletter.")
		return
	}
	contact, kept, err := s.Store.KeepContact(r.Context(), reference.ContactID, reference.NewsletterID)
	if err != nil {
		s.Log.Error("keep failed", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
	if !kept || contact.Status != model.ContactActive {
		notice(w, http.StatusNotFound, "This link doesn't work",
			"It may be older than 90 days, or you unsubscribed. Sign up again to get the newsletter.")
		return
	}
	name := "the newsletter"
	if newsletter, err := s.Store.Newsletter(r.Context(), reference.NewsletterID); err == nil {
		name = newsletter.Name
	}
	renderPage(w, http.StatusOK, "keep.html", page{
		Title: "You'll keep getting it", Heading: "You'll keep getting " + name,
		Description: "Thanks for letting me know. The next issue lands in your inbox.",
	})
}
