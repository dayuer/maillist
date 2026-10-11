package rules

import (
	"strings"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/render"
	"mailcheap/internal/core/sign"
)

// RecipientEmail is one message ready for the provider.
type RecipientEmail struct {
	From           string
	To             string
	ReplyTo        string
	Subject        string
	HTML           string
	Text           string
	Headers        map[string]string // List-Unsubscribe, List-Unsubscribe-Post
	UnsubscribeURL string
	Bytes          int
}

// RecipientInput is one recipient's copy of a campaign.
type RecipientInput struct {
	AppURL     string
	Campaign   model.Campaign // subject, preview text, format, body
	Newsletter model.Newsletter
	Contact    model.Contact
	MessageID  string // "" = untracked
	Kind       model.MessageKind
	ConfirmURL string
	KeepURL    string
}

// BuildRecipientEmail renders one recipient's message. With a MessageID opens and clicks are
// tracked; the unsubscribe, confirmation and keep links go in afterwards, untouched.
func BuildRecipientEmail(s *sign.Signer, in RecipientInput) (RecipientEmail, error) {
	links, err := s.UnsubscribeLinksFor(in.AppURL, sign.Ref{
		ContactID:    in.Contact.ID,
		NewsletterID: in.Newsletter.ID,
	})
	if err != nil {
		return RecipientEmail{}, err
	}
	rendered := render.Email(render.EmailInput{
		Format:      in.Campaign.Format,
		Body:        in.Campaign.Body,
		Subject:     in.Campaign.Subject,
		PreviewText: in.Campaign.PreviewText,
		Newsletter:  in.Newsletter,
		WebURL:      IssueWebURL(in.AppURL, in.Campaign, in.Newsletter),
	})
	html := rendered.HTML
	if in.MessageID != "" {
		html = render.AddTracking(s, html, in.AppURL, in.MessageID)
	}
	fillLinks := func(body string) string {
		body = strings.ReplaceAll(body, render.UnsubscribePlaceholder, links.PageURL)
		body = strings.ReplaceAll(body, ConfirmURLPlaceholder, in.ConfirmURL)
		return strings.ReplaceAll(body, KeepURLPlaceholder, in.KeepURL)
	}
	html = fillLinks(html)

	return RecipientEmail{
		From: FormatFromAddress(in.Newsletter.FromName, in.Newsletter.FromEmail),
		To:   in.Contact.Email,
		// Upstream reads campaign.replyTo first, but its Campaign schema has no such field, so the
		// value is always the newsletter's.
		ReplyTo:        in.Newsletter.ReplyTo,
		Subject:        in.Campaign.Subject,
		HTML:           html,
		Text:           fillLinks(rendered.Text),
		Headers:        links.Headers,
		UnsubscribeURL: links.PageURL,
		Bytes:          len(html),
	}, nil
}
