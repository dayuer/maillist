package httpsrv

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mailcheap/internal/config"
)

// 上传的图片由读者面服务，而且只服务那一个形状的名字。
//
// The name is an id and one of four extensions, checked before the path is built: that is what keeps
// this handler unable to read anything but an upload, whatever the request says. The id is the
// content, so the response is immutable.
func TestUploadsAreServedImmutablyAndOnlyByName(t *testing.T) {
	dir := t.TempDir()
	uploads := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(uploads, 0o750); err != nil {
		t.Fatal(err)
	}
	const name = "0123456789abcdef01234567.png"
	//nolint:gosec // G306: a fixture the reader surface serves, not a secret
	if err := os.WriteFile(filepath.Join(uploads, name), []byte("not a real png, the bytes are not the point"), 0o640); err != nil {
		t.Fatal(err)
	}
	// A file the pattern does not allow, and one the database would not have written.
	//nolint:gosec // G306: a fixture that must not be served, not a secret
	if err := os.WriteFile(filepath.Join(uploads, "secret.txt"), []byte("nothing to see"), 0o640); err != nil {
		t.Fatal(err)
	}
	server := newTestServerWith(t, func(cfg *config.Config) { cfg.DataDir = dir })

	response := server.get(t, "/uploads/"+name)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("content type = %q", got)
	}
	if got := response.Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("cache control = %q", got)
	}

	for _, path := range []string{
		"/uploads/secret.txt",
		"/uploads/" + strings.TrimSuffix(name, ".png"),
		"/uploads/not-an-id.png",
		"/uploads/0123456789abcdef01234567.exe",
		"/uploads/0123456789abcdef01234567.PNG",
		"/uploads/",
	} {
		other := server.get(t, path)
		_ = other.Body.Close()
		if other.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, other.StatusCode)
		}
	}

	// A traversal attempt never reaches the handler: the mux cleans the path first (a 307 to a path
	// that is not registered). Either way the database is not served.
	traversal := server.get(t, "/uploads/../mailcheap.db")
	_ = traversal.Body.Close()
	if traversal.StatusCode == http.StatusOK {
		t.Errorf("GET /uploads/../mailcheap.db = 200")
	}

	// A valid name whose file is not there is a 404 as well, rather than an error the reader sees.
	missing := server.get(t, "/uploads/aaaaaaaaaaaaaaaaaaaaaaaa.png")
	_ = missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("GET a missing upload = %d, want 404", missing.StatusCode)
	}
}
