package archive

import "testing"

// lib/archive-hosts.js has no test file of its own upstream; these cases are its four functions, read
// off the source. The relative-versus-absolute distinction is the part that matters: an archive page
// is cached once for every host, so its own links are relative, while the API hands out addresses
// that work from anywhere.
func TestParseHosts(t *testing.T) {
	hosts := ParseHosts("news.example.com=weekly, letters.example.org=another ,bad,=x,y=")
	if len(hosts) != 2 {
		t.Fatalf("hosts = %v", hosts)
	}
	if hosts["news.example.com"] != "weekly" || hosts["letters.example.org"] != "another" {
		t.Fatalf("hosts = %v", hosts)
	}
	// Blank and half-written pairs are ignored, as upstream ignores them.
	if got := ParseHosts(""); len(got) != 0 {
		t.Fatalf("an empty value gave %v", got)
	}
	// A repeated host takes the last entry, as a Map does.
	if got := ParseHosts("a.example=x,b.example=y,a.example=z")["a.example"]; got != "z" {
		t.Fatalf("a.example = %q, want z", got)
	}
	// Case and spaces are folded on both sides.
	if got := ParseHosts(" NEWS.example.com = Weekly ")["news.example.com"]; got != "weekly" {
		t.Fatalf("got %q", got)
	}
}

func TestSlugForHost(t *testing.T) {
	const hosts = "news.example.com=weekly"
	if slug, ok := SlugForHost("news.example.com", hosts); !ok || slug != "weekly" {
		t.Fatalf("got %q %v", slug, ok)
	}
	// The port is not part of the hostname.
	if slug, ok := SlugForHost("news.example.com:8080", hosts); !ok || slug != "weekly" {
		t.Fatalf("with a port: %q %v", slug, ok)
	}
	if slug, ok := SlugForHost("NEWS.example.com", hosts); !ok || slug != "weekly" {
		t.Fatalf("uppercase: %q %v", slug, ok)
	}
	if _, ok := SlugForHost("other.example.com", hosts); ok {
		t.Fatal("an unknown host matched")
	}
}

func TestHostForSlug(t *testing.T) {
	const hosts = "news.example.com=weekly,letters.example.org=another"
	if host, ok := HostForSlug("weekly", hosts); !ok || host != "news.example.com" {
		t.Fatalf("got %q %v", host, ok)
	}
	if host, ok := HostForSlug("Another", hosts); !ok || host != "letters.example.org" {
		t.Fatalf("got %q %v", host, ok)
	}
	if _, ok := HostForSlug("third", hosts); ok {
		t.Fatal("a newsletter with no host matched")
	}
}

func TestForNewsletter(t *testing.T) {
	// With a host of its own: everything is absolute, and the index is the host's root.
	withHost := ForNewsletter("weekly", "news.example.com=weekly", "https://mail.example.com")
	if withHost.Host != "news.example.com" {
		t.Fatalf("host = %q", withHost.Host)
	}
	if withHost.Index != "https://news.example.com/" || withHost.SiteIndex != "https://news.example.com/" {
		t.Fatalf("index = %q / %q", withHost.Index, withHost.SiteIndex)
	}
	if got := withHost.Post("issue-12"); got != "https://news.example.com/p/issue-12" {
		t.Fatalf("post = %q", got)
	}
	if got := withHost.AbsolutePost("issue-12"); got != "https://news.example.com/p/issue-12" {
		t.Fatalf("absolute post = %q", got)
	}
	if got := withHost.Author("jane"); got != "https://news.example.com/authors/jane" {
		t.Fatalf("author = %q", got)
	}

	// Without one: the links are relative (the page is cached per host) and only the absolute forms
	// carry the deployment's address.
	withoutHost := ForNewsletter("weekly", "", "https://mail.example.com")
	if withoutHost.Host != "" {
		t.Fatalf("host = %q", withoutHost.Host)
	}
	if withoutHost.Index != "/archive/weekly" {
		t.Fatalf("index = %q", withoutHost.Index)
	}
	if withoutHost.SiteIndex != "https://mail.example.com/archive/weekly" {
		t.Fatalf("site index = %q", withoutHost.SiteIndex)
	}
	if got := withoutHost.Post("issue-12"); got != "/archive/weekly/p/issue-12" {
		t.Fatalf("post = %q", got)
	}
	if got := withoutHost.AbsolutePost("issue-12"); got != "https://mail.example.com/archive/weekly/p/issue-12" {
		t.Fatalf("absolute post = %q", got)
	}
	if got := withoutHost.AbsoluteAuthor("jane"); got != "https://mail.example.com/archive/weekly/authors/jane" {
		t.Fatalf("absolute author = %q", got)
	}
}
