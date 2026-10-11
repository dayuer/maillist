package httpsrv

import (
	"net/http"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/sign"
)

// handleConfirmPage shows the confirmation page. The URL carries the token, so the page is noindex
// and the link is a form the reader submits — a scanner that opens the page confirms nothing.
func (s *Server) handleConfirmPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	reference, ok := s.Signer.VerifyContactToken(sign.KindConfirm, token, sign.ConfirmMaxAge, s.clock())
	if !ok {
		notice(w, http.StatusNotFound, "This link doesn't work",
			"It may be older than 30 days. Sign up again and use the newest confirmation email.")
		return
	}
	newsletter, err := s.Store.Newsletter(r.Context(), reference.NewsletterID)
	if err != nil {
		notice(w, http.StatusNotFound, "This link doesn't work",
			"It may be older than 30 days. Sign up again and use the newest confirmation email.")
		return
	}
	renderPage(w, http.StatusOK, "confirm.html", page{
		Title: "Confirm your subscription", Heading: "Confirm your subscription",
		Description: "One click and you're in. Until then, you won't get " + newsletter.Name + ".",
		Action:      "/confirm?token=" + token, Button: "Confirm my subscription",
	})
}

// handleConfirm is the button. It confirms a pending contact and, when it is the click that did it,
// sends the welcome email the newsletter would otherwise have sent at signup.
func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	reference, ok := s.Signer.VerifyContactToken(sign.KindConfirm, token, sign.ConfirmMaxAge, s.clock())
	if !ok {
		notice(w, http.StatusNotFound, "This link doesn't work",
			"It may be older than 30 days. Sign up again and use the newest confirmation email.")
		return
	}
	contact, confirmed, err := s.Store.ConfirmContact(r.Context(), reference.ContactID, reference.NewsletterID)
	if err != nil || contact.Status != model.ContactActive {
		notice(w, http.StatusNotFound, "This link doesn't work",
			"It may be older than 30 days. Sign up again and use the newest confirmation email.")
		return
	}

	if confirmed && s.Config.SendingOn {
		if newsletter, err := s.Store.Newsletter(r.Context(), reference.NewsletterID); err == nil {
			if _, err := s.Mailer.QueueWelcome(r.Context(), newsletter, contact); err != nil {
				s.Log.Error("welcome email failed", "contact", contact.ID, "error", err)
			}
		}
	}
	renderPage(w, http.StatusOK, "confirm.html", page{
		Title: "Confirmed", Heading: "You're in",
		Description: "Your next issue lands in your inbox.",
	})
}
