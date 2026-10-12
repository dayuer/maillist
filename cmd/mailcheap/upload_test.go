package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mailcheap/internal/uploads"
)

// 最小但合法的 PNG：一张 1×1 的完整文件。魔数与格式的判据本身在 internal/uploads 里测。
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"),
	[]byte("\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4"+
		"\x89\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND"+
		"\xaeB`\x82")...)

var webpBytes = []byte("RIFF\x24\x00\x00\x00WEBPVP8 " + strings.Repeat("\x00", 16))

// 上传落盘并打印 URL：文件在 <data-dir>/uploads/<id>.<ext>，行里也有一份记录。
func TestUploadCommandStoresTheImage(t *testing.T) {
	dir := uploadTestEnv(t)
	runMailcheap(t, "init")

	source := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(source, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	url := strings.TrimSpace(runMailcheap(t, "upload", source))
	if !strings.HasPrefix(url, "https://mail.example.com/uploads/") || !strings.HasSuffix(url, ".png") {
		t.Fatalf("upload printed %q", url)
	}
	stored := filepath.Join(dir, "uploads", filepath.Base(url))
	content, err := os.ReadFile(stored) //nolint:gosec // G304: the path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, pngBytes) {
		t.Fatal("the stored file is not the one that was uploaded")
	}

	// The name is an id and an extension, which is what the URL route accepts.
	name := filepath.Base(url)
	if len(name) != len("0123456789abcdef01234567")+len(".png") {
		t.Fatalf("stored as %q", name)
	}
}

// 非图片、超大、空文件都被拒，且什么都不落下。拒绝的判据与话术都来自 internal/uploads。
func TestUploadCommandRefusesWhatIsNotAnImage(t *testing.T) {
	dir := uploadTestEnv(t)
	runMailcheap(t, "init")

	text := filepath.Join(dir, "not-an-image.png")
	if err := os.WriteFile(text, []byte("just some text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runMailcheapError(t, "upload", text); !strings.Contains(out, "isn't a valid image") {
		t.Fatalf("err = %q", out)
	}

	big := filepath.Join(dir, "big.png")
	huge := append(append([]byte{}, pngBytes...), bytes.Repeat([]byte("x"), uploads.MaxBytes)...)
	if err := os.WriteFile(big, huge, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runMailcheapError(t, "upload", big); !strings.Contains(out, "images must be under") {
		t.Fatalf("err = %q", out)
	}

	empty := filepath.Join(dir, "empty.png")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runMailcheapError(t, "upload", empty); !strings.Contains(out, "empty") {
		t.Fatalf("err = %q", out)
	}

	if entries, err := os.ReadDir(filepath.Join(dir, "uploads")); err == nil && len(entries) != 0 {
		t.Fatalf("uploads holds %d files after three refusals", len(entries))
	}
}

// 上传的 JSON 里有 id、url 与类型。
func TestUploadJSON(t *testing.T) {
	dir := uploadTestEnv(t)
	runMailcheap(t, "init")

	source := filepath.Join(dir, "logo.webp")
	if err := os.WriteFile(source, webpBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal([]byte(runMailcheap(t, "upload", source, "--json")), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.MediaType != "image/webp" || !strings.HasSuffix(parsed.URL, ".webp") {
		t.Fatalf("parsed = %+v", parsed)
	}
}

// uploadTestEnv points the CLI at a temporary data directory.
func uploadTestEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("APP_URL", "https://mail.example.com")
	t.Setenv("MAILCHEAP_DATA_DIR", dir)
	t.Setenv("MAILCHEAP_DB", filepath.Join(dir, "mailcheap.db"))
	t.Setenv("MAILCHEAP_FAKE_SES_LOG", filepath.Join(dir, "sent.jsonl"))
	return dir
}
