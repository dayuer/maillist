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
// Go decodes percent-escapes into url.Value.Path, as WHATWG does, so an encoded path is recognised
// here as well.
func UnsubscribePath(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return unsubscribePathPattern.MatchString(path.Clean(parsed.Path))
}
