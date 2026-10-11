package httpsrv

import (
	"net/http"
	"net/url"
	"time"

	"mailcheap/internal/core/model"
)

// unsubscribeLimit is upstream's RATE_LIMIT: Gmail and Yahoo send one-click requests from shared
// mail servers, so it stays generous.
var unsubscribeLimit = Limit{Name: "unsubscribe", Count: 60, Window: time.Minute}

// subscribedStatuses are the statuses an unsubscribe moves.
var subscribedStatuses = []model.ContactStatus{model.ContactActive, model.ContactDormant}

// handleUnsubscribePage shows the page the footer links to. A mail app that opens a
// List-Unsubscribe URL in a browser must change nothing on GET.
func (s *Server) handleUnsubscribePage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	status := r.URL.Query().Get("status")
	reference, ok := s.Signer.VerifyUnsubscribe(token)
	if !ok {
		notice(w, http.StatusNotFound, "This link doesn't work",
			"Open the unsubscribe link from the email again, or reply to the email to ask to be removed.")
		return
	}

	name := "this newsletter"
	isSubscribed := true
	if newsletter, err := s.Store.Newsletter(r.Context(), reference.NewsletterID); err == nil {
		if newsletter.Name != "" {
			name = newsletter.Name
		}
		if contact, err := s.Store.Contact(r.Context(), reference.ContactID); err == nil {
			isSubscribed = contact.Status == model.ContactActive || contact.Status == model.ContactDormant
		}
	}

	form := page{
		Title: "Unsubscribe", Action: "/api/unsubscribe?token=" + url.QueryEscape(token),
		Hidden: map[string]string{"source": "page"}, Button: "Unsubscribe",
	}
	switch {
	case status == "done" || !isSubscribed:
		form.Heading = "You're unsubscribed"
		form.Description = "You won't receive emails from " + name + " anymore."
		form.Action, form.Hidden, form.Button = "", nil, ""
		renderPage(w, http.StatusOK, "notice.html", form)
	case status == "error":
		form.Heading = "Something went wrong"
		form.Description = "You're still subscribed to " + name + ". Try again in a minute."
		renderPage(w, http.StatusOK, "notice.html", form)
	default:
		form.Heading = "Unsubscribe from " + name + "?"
		form.Description = "You'll stop receiving its emails."
		renderPage(w, http.StatusOK, "notice.html", form)
	}
}

// handleUnsubscribe is the RFC 8058 one-click endpoint and the confirmation page's form target.
func (s *Server) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	// The page form announces itself with source=page; an RFC 8058 POST sends no body at all.
	isPageForm := false
	if err := r.ParseForm(); err == nil {
		isPageForm = r.PostFormValue("source") == "page"
	}

	respond := func(status int, pageStatus string) {
		if !isPageForm {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			return
		}
		target := "/unsubscribe?"
		switch pageStatus {
		case "done":
			target += "token=" + url.QueryEscape(token) + "&status=done"
		case "invalid":
			target += "status=invalid"
		default:
			target += "token=" + url.QueryEscape(token) + "&status=error"
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}

	if s.Limiter.Limited(unsubscribeLimit, s.clientIP(r), s.clock()) {
		rateLimited(w, unsubscribeLimit)
		return
	}
	reference, ok := s.Signer.VerifyUnsubscribe(token)
	if !ok {
		respond(http.StatusBadRequest, "invalid")
		return
	}
	if err := s.unsubscribe(r, reference.ContactID, reference.NewsletterID); err != nil {
		s.Log.Error("unsubscribe failed", "error", err)
		respond(http.StatusInternalServerError, "error")
		return
	}
	respond(http.StatusOK, "done")
}

// unsubscribe moves one contact out of the subscribed statuses. A bounce or complaint stays as it
// is: those block sending already, and they are not the reader's choice to reverse.
func (s *Server) unsubscribe(r *http.Request, contactID, newsletterID string) error {
	moved, err := s.Store.UnsubscribeContact(r.Context(), contactID, newsletterID, subscribedStatuses)
	if err != nil {
		return err
	}
	if !moved {
		// Already unsubscribed: the endpoint stays idempotent, so a mail app's retry is a 200.
		return nil
	}
	s.Log.Info("contact unsubscribed", "contact", contactID, "newsletter", newsletterID)
	// The link carries no campaign, so the unsubscribe counts against the latest issue they got.
	if err := s.Store.CreditLatestCampaignUnsubscribe(r.Context(), contactID, newsletterID); err != nil {
		s.Log.Warn("could not credit the campaign", "error", err)
	}
	return nil
}
