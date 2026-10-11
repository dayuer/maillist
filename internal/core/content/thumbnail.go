package content

import (
	"regexp"
	"strconv"
	"strings"
)

// Upstream SKIPPED_URL_PATTERN: tracking pixels and chrome, not the picture of the issue.
var skippedImagePattern = regexp.MustCompile(`(?i)(pixel|tracking|spacer|beacon|1x1|favicon|emoji)`)

// Narrower images are icons, avatars and social buttons, not the picture of the issue.
const minImageWidth = 120

var (
	imageSrcPattern   = regexp.MustCompile(`(?i)\bsrc=["'](https://[^"']+)["']`)
	imageWidthPattern = regexp.MustCompile(`(?i)\bwidth=["']?(\d+)`)
)

// FirstImageURL is the src of the first <img> with an https:// src, skipping tracking pixels and
// images narrower than 120px, or "" when there is none. Only tags up to the last ">" are read: one
// before another's ">" has a src only if that one does.
func FirstImageURL(html string) string {
	end := strings.LastIndex(html, ">") + 1
	if end <= 0 {
		return ""
	}
	for _, tag := range imgTagPattern.FindAllString(html[:end], -1) {
		m := imageSrcPattern.FindStringSubmatch(tag)
		if m == nil || skippedImagePattern.MatchString(m[1]) {
			continue
		}
		if w := imageWidthPattern.FindStringSubmatch(tag); w != nil {
			if n, _ := strconv.Atoi(w[1]); n != 0 && n < minImageWidth {
				continue
			}
		}
		return m[1]
	}
	return ""
}
