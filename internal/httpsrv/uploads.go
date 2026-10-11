package httpsrv

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// uploadNamePattern is the only thing that may be asked for: an id and one of the four extensions
// an upload can have. Checking it before the path is built is what keeps the handler unable to read
// anything but an upload, whatever the request says.
var uploadNamePattern = regexp.MustCompile(`^[0-9a-f]{24}\.(png|jpg|gif|webp)$`)

// uploadMediaTypes maps the extension to what the bytes are. The upload command decided this from the
// signature when it stored the file, so the extension is not a guess.
var uploadMediaTypes = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "gif": "image/gif", "webp": "image/webp",
}

// handleUpload serves GET /uploads/{name}: the images a campaign body points at.
//
// The id in the name is what the content is, and it never changes, so the response is immutable and
// can be cached for as long as the reader's browser likes. Nothing else about the file is public:
// no directory listing, no other extension, no path.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !uploadNamePattern.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	extension := strings.TrimPrefix(filepath.Ext(name), ".")
	mediaType, ok := uploadMediaTypes[extension]
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.Config.DataDir, "uploads", name)
	file, err := os.Open(path) //nolint:gosec // G304: the name is checked against uploadNamePattern above
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, info.ModTime(), file)
}
