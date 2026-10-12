package uploads

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mailcheap/internal/store"
)

// 最小但合法的四种图片文件头，以及一张 1×1 的完整 PNG。
var (
	pngBytes = append([]byte("\x89PNG\r\n\x1a\n"),
		[]byte("\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4"+
			"\x89\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND"+
			"\xaeB`\x82")...)
	gifBytes  = []byte("GIF89a" + strings.Repeat("\x00", 10))
	webpBytes = []byte("RIFF\x24\x00\x00\x00WEBPVP8 " + strings.Repeat("\x00", 16))
	jpegBytes = []byte("\xff\xd8\xff\xe0" + strings.Repeat("\x00", 16))
)

// 类型由头几个字节决定，不是文件名：一个叫 .png 的文本文件不能进来。
//
// This is what keeps something that is not a picture out of the directory the newsletter's HTML
// points at, and both callers — the CLI reading a path and the MCP tool reading a base64 field — get
// it from the same table.
func TestDetectReadsTheImageTypeFromTheBytes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		content   []byte
		extension string
		mediaType string
	}{
		{"a.png", pngBytes, "png", "image/png"},
		{"a.gif", gifBytes, "gif", "image/gif"},
		{"a.webp", webpBytes, "webp", "image/webp"},
		{"a.jpg", jpegBytes, "jpg", "image/jpeg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := Detect(tc.content)
			if !ok || kind.Extension != tc.extension || kind.MediaType != tc.mediaType {
				t.Fatalf("got %+v %v, want %s %s", kind, ok, tc.extension, tc.mediaType)
			}
		})
	}

	// A text file with an image name, a RIFF that is not WebP (it is a WAV), and an empty file.
	for _, content := range [][]byte{
		[]byte("<html>not a picture</html>"),
		[]byte("RIFF\x24\x00\x00\x00WAVE" + strings.Repeat("\x00", 16)),
		{},
	} {
		if kind, ok := Detect(content); ok {
			t.Errorf("%q was taken for a %s", content[:min(len(content), 12)], kind.MediaType)
		}
	}
}

// 太大、为空、不是图片：三种拒绝，且都不落盘。
func TestValidateRefusesWhatIsNotAnImage(t *testing.T) {
	if _, err := Validate(nil); err == nil {
		t.Error("an empty file was accepted")
	}
	if _, err := Validate(append(append([]byte{}, pngBytes...), bytes.Repeat([]byte("x"), MaxBytes)...)); err == nil {
		t.Error("a file over the limit was accepted")
	}
	if _, err := Validate([]byte("just some text")); err == nil {
		t.Error("a text file was accepted")
	}
}

// 落盘的位置与命名是 URL 路由认得的形状：uploads/<id>.<ext>，行里记着同一件事。
func TestSaveWritesTheFileAndTheRow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	upload, err := Save(ctx, db, dir, "/somewhere/logo.png", pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	name := Name(upload)
	if name != upload.ID+".png" || len(upload.ID) != 24 {
		t.Fatalf("stored as %q", name)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "uploads", name)) //nolint:gosec // G304: the path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, pngBytes) {
		t.Fatal("the stored file is not the one that was uploaded")
	}
	if upload.FileName != "logo.png" || upload.Slug != "logo" || upload.MediaType != "image/png" {
		t.Fatalf("row = %+v", upload)
	}
	if got := URL("https://mail.example.com/", upload); got != "https://mail.example.com/uploads/"+name {
		t.Fatalf("url = %q", got)
	}

	// 不是图片的直接拒绝，且什么都不落下。
	if _, err := Save(ctx, db, dir, "notes.txt", []byte("just some text")); err == nil {
		t.Fatal("a text file was stored")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("uploads holds %d files after a refusal", len(entries))
	}
}
