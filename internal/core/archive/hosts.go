// Package archive is the public issue archive: its hosts, its SEO and its content. Phase 4 adds the
// host map, because the v1 API returns absolute archive URLs; the pages themselves arrive in phase 7.
package archive

import "strings"

// ParseHosts is upstream parseArchiveHosts: "news.example.com=weekly,letters.example.org=another"
// maps a hostname to the newsletter it serves. A pair missing either half is ignored, as upstream
// ignores it, because a half-written entry is not a configuration mistake worth refusing to start
// over.
func ParseHosts(value string) map[string]string {
	hosts := map[string]string{}
	for _, pair := range strings.Split(value, ",") {
		host, slug, found := strings.Cut(pair, "=")
		host = strings.ToLower(strings.TrimSpace(host))
		slug = strings.ToLower(strings.TrimSpace(slug))
		if found && host != "" && slug != "" {
			// A repeated host takes the last entry's newsletter, as a Map does upstream.
			hosts[host] = slug
		}
	}
	return hosts
}

// SlugForHost is upstream getArchiveSlugForHost: which newsletter a hostname serves, with the port
// and the case taken off.
func SlugForHost(host, hostsValue string) (string, bool) {
	hostname := strings.ToLower(strings.TrimSpace(host))
	if colon := strings.Index(hostname, ":"); colon >= 0 {
		hostname = hostname[:colon]
	}
	slug, ok := ParseHosts(hostsValue)[hostname]
	return slug, ok
}

// HostForSlug is upstream getArchiveHost: the hostname that serves a newsletter, if any.
func HostForSlug(slug, hostsValue string) (string, bool) {
	for host, hostSlug := range ParseHosts(hostsValue) {
		if hostSlug == strings.ToLower(strings.TrimSpace(slug)) {
			return host, true
		}
	}
	return "", false
}

// URLs is upstream getArchiveUrls' result. Without a host of its own the archive lives under
// APP_URL at /archive/<slug>, and the absolute forms are what another site's API consumer can use.
type URLs struct {
	Host string // "" when the newsletter has no domain of its own
	// Index is the archive's own index link: relative without an archive host, as upstream's is,
	// because an archive page is cached once for every host and cannot depend on the request.
	Index string
	// SiteIndex is the same thing as an address a browser can follow from anywhere.
	SiteIndex string
	// base is the prefix the relative links use, absolute the one the absolute links use.
	base     string
	absolute string
}

// ForNewsletter builds the URLs for one newsletter. host is the ARCHIVE_HOSTS value and appURL is
// the deployment's own address; both are passed in rather than read from the environment, so this
// package stays pure.
func ForNewsletter(slug, hostsValue, appURL string) URLs {
	if host, ok := HostForSlug(slug, hostsValue); ok {
		base := "https://" + host
		return URLs{Host: host, Index: base + "/", SiteIndex: base + "/", base: base, absolute: base}
	}
	base := "/archive/" + slug
	absolute := appURL + base
	return URLs{Index: base, SiteIndex: absolute, base: base, absolute: absolute}
}

// Post is the archive page for one issue: relative when the newsletter has no archive host.
func (u URLs) Post(postSlug string) string { return u.base + "/p/" + postSlug }

// AbsolutePost is Post as an address a browser can follow from anywhere.
func (u URLs) AbsolutePost(postSlug string) string { return u.absolute + "/p/" + postSlug }

// Author is an author page.
func (u URLs) Author(authorSlug string) string { return u.base + "/authors/" + authorSlug }

// AbsoluteAuthor is Author as an address a browser can follow from anywhere.
func (u URLs) AbsoluteAuthor(authorSlug string) string { return u.absolute + "/authors/" + authorSlug }

// hostPaths are the archive's own paths on a newsletter's domain. Next.js served them as
// /sitemap and /robots because of a rewrite; this port gives the domain the files themselves, and
// keeps upstream's two mappings for the links it prints.
var hostPaths = map[string]string{"/sitemap": "/sitemap.xml", "/robots": "/robots.txt"}

// HostURL is upstream getArchiveHostUrl: where an application-domain /archive path lives once the
// newsletter routes its own domain here, or ok = false when it has no domain (or the path is not an
// archive path at all).
func HostURL(pathname, search, hostsValue string) (string, bool) {
	rest, found := strings.CutPrefix(pathname, "/archive/")
	if !found {
		return "", false
	}
	slug, path, _ := strings.Cut(rest, "/")
	host, ok := HostForSlug(slug, hostsValue)
	if !ok {
		return "", false
	}
	path = "/" + path
	if mapped, ok := hostPaths[strings.TrimSuffix(path, "/")]; ok {
		path = mapped
	}
	return "https://" + host + path + search, true
}
