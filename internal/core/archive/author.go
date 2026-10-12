package archive

import (
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/slug"
)

// Author is upstream getArchiveAuthor: one person writes each newsletter, so the author is the From
// name, with the photo and bio set on the newsletter. There is no user table to look anyone up in.
type Author struct {
	Name     string
	Slug     string
	Bio      string
	ImageURL string
}

// The author's name is what the From header says, falling back to the newsletter's own name when the
// operator never set one: a page with no author is worse than one attributed to the publication.
func AuthorOf(n model.Newsletter) Author {
	name := n.FromName
	if name == "" {
		name = n.Name
	}
	authorSlug := slug.Slug(name, 60)
	if authorSlug == "" {
		// The slug is a URL segment: an author whose name is punctuation still needs a page.
		authorSlug = "author"
	}
	image := n.AuthorImageURL
	if image == "" {
		image = n.ImageURL
	}
	return Author{Name: name, Slug: authorSlug, Bio: n.AuthorBio, ImageURL: image}
}
