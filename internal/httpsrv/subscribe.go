package httpsrv

import (
	"net/http"
	"net/url"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/render"
	"nibwire/internal/core/validate"
)

// subscribeLimits are upstream's PER_IP and PER_NEWSLETTER. The newsletter one only alerts: refusing
// signups past a shared limit would let one flooder block every real signup.
var (
	subscribeIPLimit         = Limit{Name: "subscribe-ip", Count: 5, Window: 10 * time.Minute}
	subscribeNewsletterLimit = Limit{Name: "subscribe-newsletter", Count: 300, Window: 10 * time.Minute}
)

// handleSubscribePage shows the signup form for a newsletter that takes signups.
func (s *Server) handleSubscribePage(w http.ResponseWriter, r *http.Request) {
	newsletter, err := s.Store.NewsletterBySlug(r.Context(), r.PathValue("slug"))
	if err != nil || !newsletter.ArchiveEnabled {
		notice(w, http.StatusNotFound, "Not found", "This newsletter is not taking signups.")
		return
	}
	renderPage(w, http.StatusOK, "subscribe.html", page{
		Title: newsletter.Name, Heading: "Subscribe to " + newsletter.Name,
		Description: newsletter.Description,
		Action:      "/subscribe/" + url.PathEscape(newsletter.Slug),
		Button:      "Subscribe",
	})
}

// handleSubscribe is upstream subscribe(): a honeypot, an IP limit, then the newsletter's own limit
// and the signup email the newsletter's settings call for.
func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	form := func(heading, description, action, button string) page {
		return page{Title: "Subscribe", Heading: heading, Description: description,
			Action: action, Button: button}
	}
	backToForm := func(heading, description string) page {
		return form(heading, description, "/subscribe/"+url.PathEscape(slug), "Subscribe")
	}
	result := func(heading, description string) page {
		return page{Title: "Subscribed", Heading: heading, Description: description}
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}
	// People never see this field; bots fill in every input they find.
	if r.PostFormValue("website") != "" {
		renderPage(w, http.StatusOK, "subscribe.html", result("Check your inbox",
			"If that address is new here, a message is on its way."))
		return
	}
	email := validate.NormalizeEmail(r.PostFormValue("email"))
	if email == "" {
		renderPage(w, http.StatusOK, "subscribe.html", backToForm("Enter a valid email",
			"That address doesn't look like one we can send to."))
		return
	}
	if s.Limiter.Limited(subscribeIPLimit, s.clientIP(r), s.clock()) {
		renderPage(w, http.StatusTooManyRequests, "subscribe.html", backToForm(
			"Too many signups", "Too many signups from your network. Try again in a few minutes."))
		return
	}

	newsletter, err := s.Store.NewsletterBySlug(r.Context(), slug)
	if err != nil || !newsletter.ArchiveEnabled {
		notice(w, http.StatusNotFound, "Not found", "This newsletter is not taking signups.")
		return
	}
	if s.Limiter.Limited(subscribeNewsletterLimit, newsletter.Slug, s.clock()) {
		// Phase 7 turns this into a sendAlert; for now it is worth a loud log line.
		s.Log.Warn("website signup flood", "newsletter", newsletter.Slug,
			"limit", subscribeNewsletterLimit.Count)
	}

	contact, created, err := s.Store.UpsertContact(r.Context(), newsletter, email, "website", true)
	if err != nil {
		s.Log.Error("signup failed", "error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	if s.Config.SendingOn {
		switch {
		case contact.Status == model.ContactPending:
			if _, err := s.Mailer.QueueConfirmation(r.Context(), newsletter, contact); err != nil {
				s.Log.Error("confirmation email failed", "contact", contact.ID, "error", err)
			}
		case created && contact.Status == model.ContactActive:
			if _, err := s.Mailer.QueueWelcome(r.Context(), newsletter, contact); err != nil {
				s.Log.Error("welcome email failed", "contact", contact.ID, "error", err)
			}
		}
	}

	if contact.Status != model.ContactPending {
		renderPage(w, http.StatusOK, "subscribe.html", result("You're subscribed",
			"Your next issue of "+newsletter.Name+" lands in your inbox."))
		return
	}
	// Confirmed opt-in: tell them where to look, with a button straight into their webmail.
	name, inboxURL := render.Inbox(email, newsletter.FromEmail)
	heading := "Confirm your subscription"
	description := "One click and you're in. We've sent a confirmation email to " + email + "."
	if name != "" {
		description = "One click and you're in. Check " + name + " for our confirmation email."
	}
	renderPage(w, http.StatusOK, "subscribe.html", page{
		Title: "Confirm your subscription", Heading: heading, Description: description,
		Link: inboxURL, LinkLabel: "Open " + name,
	})
}
