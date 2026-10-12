package archive

import (
	"net/url"
	"strings"
)

// Platform is the site a social link points at. The archive shows the profiles on the author page and
// search engines read them as the author's other homes, so what matters here is the identity, not the
// mark: upstream draws a brand icon per platform, which is the front end's business.
type Platform struct {
	ID    string
	Label string
}

// platforms is upstream PLATFORMS, without the SVG paths.
var platforms = []struct {
	id    string
	label string
	hosts []string
}{
	{"x", "X", []string{"x.com", "twitter.com"}},
	{"linkedin", "LinkedIn", []string{"linkedin.com"}},
	{"youtube", "YouTube", []string{"youtube.com", "youtu.be"}},
	{"tiktok", "TikTok", []string{"tiktok.com"}},
	{"instagram", "Instagram", []string{"instagram.com"}},
	{"github", "GitHub", []string{"github.com"}},
	{"threads", "Threads", []string{"threads.net", "threads.com"}},
	{"facebook", "Facebook", []string{"facebook.com", "fb.com"}},
}

// PlatformOf is upstream getSocialPlatform: the platform a link belongs to, or the site itself. ok is
// false for something that is not a URL at all.
func PlatformOf(link string) (Platform, bool) {
	hostname := hostnameOf(link)
	if hostname == "" {
		return Platform{}, false
	}
	for _, platform := range platforms {
		for _, host := range platform.hosts {
			if hostname == host || strings.HasSuffix(hostname, "."+host) {
				return Platform{ID: platform.id, Label: platform.label}, true
			}
		}
	}
	// Unknown sites keep their hostname, which is what the page shows.
	return Platform{ID: "website", Label: hostname}, true
}

// hostnameOf is upstream hostnameOf: the registrable-ish host, with the www and m prefixes taken off
// so that www.linkedin.com and m.youtube.com are the platforms they are.
func hostnameOf(link string) string {
	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	hostname := strings.ToLower(parsed.Hostname())
	for _, prefix := range []string{"www.", "m."} {
		hostname = strings.TrimPrefix(hostname, prefix)
	}
	return hostname
}

// XHandle is upstream getXHandle: the @name in an X profile link, which Open Graph wants as the
// site's and the author's account.
func XHandle(socialLinks []string) string {
	for _, link := range socialLinks {
		platform, ok := PlatformOf(link)
		if !ok || platform.ID != "x" {
			continue
		}
		parsed, err := url.Parse(strings.TrimSpace(link))
		if err != nil {
			continue
		}
		for _, segment := range strings.Split(parsed.Path, "/") {
			if segment != "" {
				return "@" + segment
			}
		}
	}
	return ""
}
