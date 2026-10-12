package validate

import (
	"net/url"
	"path"
	"regexp"
)

// unsubscribePathPattern is lib/tracking.js and lib/ses-event-mapping.js unmodified.
var unsubscribePathPattern = regexp.MustCompile(`^/(api/)?unsubscribe/?$`)

// UnsubscribePath reports whether an absolute URL points at the unsubscribe endpoint.
//
// The path is cleaned of dot segments first. Upstream reads the pathname through the WHATWG URL
// parser, which removes them — "https://host/anything/../unsubscribe" is the unsubscribe page there
// and must be exempt from link rewriting, or the one link that has to work directly would be sent
// through the click redirect (the phase 1-3 audit's FI-A2 and FI-A3).
//
// Percent-escapes are where this port is wider than upstream, left so on purpose. WHATWG's pathname
// keeps them — "%75nsubscribe" stays "%75nsubscribe" (checked against Node) — so upstream does not
// exempt that link and sends it through the click redirect, while Go's url.Parse decodes into
// URL.Path and this exempts it. The divergence only widens the exemption, never narrows it: an
// escaped unsubscribe link stays direct here. The system's own unsubscribe links are never escaped
// mid-word, so the case is a hand-written one, and a reader who clicks it reaches the page either
// way — what differs is whether that one click is counted. Both behaviors are pinned,
// in TestUnsubscribePath and TestTrackingLinkEdges.
func UnsubscribePath(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return unsubscribePathPattern.MatchString(path.Clean(parsed.Path))
}
