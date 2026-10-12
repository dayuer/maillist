package cleanup

import (
	"strings"

	"mailcheap/internal/core/model"
)

// KeepURLPlaceholder is where the "Yes, keep sending it" button's address goes. The email is rendered
// before the reader's token exists — the token is per contact — so the link is filled in at send
// time, beside the unsubscribe link and the same way.
const KeepURLPlaceholder = "{{keep_url}}"

// LastChanceContent is upstream getLastChanceContent: what a cold contact gets instead of an issue.
// One question, one button, and no tracking: the answer is the click.
func LastChanceContent(n model.Newsletter) model.Content {
	name := strings.TrimSpace(n.Name)
	if name == "" {
		name = "this newsletter"
	}
	return model.Content{
		Subject:     "Do you still want " + name + "?",
		PreviewText: "Click the button, and it keeps coming.",
		Markdown: "**Do you still want " + name + "?**\n\n" +
			"You haven't opened it in a while, so I'm checking before I stop sending it.\n\n" +
			"<a href=\"" + KeepURLPlaceholder + "\" style=\"display:inline-block;background:#18181b;" +
			"color:#ffffff;text-decoration:none;font-size:17px;font-weight:600;padding:14px 28px;" +
			"border-radius:10px;\">Yes, keep sending it</a>\n\n" +
			"If I don't hear from you, this is the last " + name + " email you'll get.",
	}
}
