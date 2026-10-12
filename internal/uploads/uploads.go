// Package uploads stores the images a campaign body points at: the four formats an email can use,
// the signature check that decides which one a file is, and the file plus row that make its URL
// resolve.
//
// It is the shared half of `mailcheap upload` and of the MCP `upload_image` tool. The two differ
// only in where the bytes come from — a path on the operator's disk, a base64 field in a tool call —
// and everything after that is the same: the signature decides the type, the file goes under
// <data-dir>/uploads/ named <id>.<ext>, and the row is what the URL resolves against.
package uploads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/store"
)

// MaxBytes is upstream MAX_UPLOAD_BYTES: the 4.5 MB a serverless function would take, rounded down.
// A file under the operator's own disk needs no limit, but an email over a few megabytes is clipped
// by the inbox anyway, so the ceiling is a service to the sender rather than a restriction on them.
const MaxBytes = 4 << 20

// The three refusals, so a caller can add its own context (the CLI names the file it read) without
// restating the rule.
var (
	ErrEmpty    = errors.New("the file is empty")
	ErrTooLarge = fmt.Errorf("images must be under %d bytes", MaxBytes)
	ErrNotImage = errors.New("this file isn't a valid image: use a PNG, JPEG, GIF or WebP")
)

// Type is one of the four formats an email can use, with the signature that has to match.
//
// app/api/uploads/route.js checks the declared content-type against the first bytes. A CLI has no
// content-type and an MCP caller cannot be trusted to have one, so the signature is what decides the
// type; the table is the same one.
type Type struct {
	MediaType string
	Extension string
	Signature string
	// Marker is a second signature at offset 8, which WebP needs: RIFF alone is also WAV and AVI.
	Marker string
}

// Types is the table, in the order a file is tested against it.
var Types = []Type{
	{MediaType: "image/png", Extension: "png", Signature: "\x89PNG"},
	{MediaType: "image/jpeg", Extension: "jpg", Signature: "\xff\xd8\xff"},
	{MediaType: "image/gif", Extension: "gif", Signature: "GIF8"},
	{MediaType: "image/webp", Extension: "webp", Signature: "RIFF", Marker: "WEBP"},
}

// MediaTypes maps an extension to what the bytes are. internal/httpsrv serves by extension, and the
// extension was decided here from the signature, so it is not a guess.
var MediaTypes = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "gif": "image/gif", "webp": "image/webp",
}

// Detect reads the signatures. The first format whose signature — and marker, where the format needs
// one — matches is the type.
func Detect(content []byte) (Type, bool) {
	for _, kind := range Types {
		if !startsWith(content, kind.Signature, 0) {
			continue
		}
		if kind.Marker != "" && !startsWith(content, kind.Marker, 8) {
			continue
		}
		return kind, true
	}
	return Type{}, false
}

// Validate is the check both callers make before anything is written, in the order the refusals read
// best: nothing at all, more than an inbox will take, not a picture.
func Validate(content []byte) (Type, error) {
	if len(content) == 0 {
		return Type{}, ErrEmpty
	}
	if len(content) > MaxBytes {
		return Type{}, ErrTooLarge
	}
	kind, ok := Detect(content)
	if !ok {
		return Type{}, ErrNotImage
	}
	return kind, nil
}

// Save writes the image under <data-dir>/uploads/ and records the row that makes its URL resolve.
//
// The file is written beside its destination and moved into place: a reader never sees a half-written
// image, and a failure leaves nothing behind for the URL to point at. fileName is what the operator
// called it — it is kept for the list and for the slug, not for the path.
func Save(ctx context.Context, db *store.DB, dataDir, fileName string, content []byte) (store.Upload, error) {
	kind, err := Validate(content)
	if err != nil {
		return store.Upload{}, err
	}
	id := ids.New()
	dir := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return store.Upload{}, err
	}
	name := id + "." + kind.Extension

	temp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return store.Upload{}, err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return store.Upload{}, err
	}
	if err := temp.Close(); err != nil {
		return store.Upload{}, err
	}
	// 0640: the server that serves it runs as the same user, and nothing else needs to read it.
	if err := os.Chmod(temp.Name(), 0o640); err != nil { //nolint:gosec // G302: a served image, not a secret
		return store.Upload{}, err
	}
	if err := os.Rename(temp.Name(), filepath.Join(dir, name)); err != nil {
		return store.Upload{}, err
	}

	base := filepath.Base(fileName)
	upload := store.Upload{
		ID: id, FileName: base,
		Slug:      rules.Slugify(strings.TrimSuffix(base, filepath.Ext(base)), 60),
		MediaType: kind.MediaType, Extension: kind.Extension,
		Bytes: len(content), CreatedAt: time.Now().UTC(),
	}
	if err := db.CreateUpload(ctx, upload); err != nil {
		// The row is what makes the URL resolvable, so a database failure takes the file with it.
		_ = os.Remove(filepath.Join(dir, name))
		return store.Upload{}, err
	}
	return upload, nil
}

// Name is the file name of a stored upload: the id and the extension, which is the only shape the
// /uploads route accepts.
func Name(u store.Upload) string { return u.ID + "." + u.Extension }

// URL is where a reader fetches the image: the deployment's own address plus the uploads route.
func URL(appURL string, u store.Upload) string {
	return strings.TrimSuffix(appURL, "/") + "/uploads/" + Name(u)
}

func startsWith(content []byte, signature string, offset int) bool {
	if offset < 0 || offset+len(signature) > len(content) {
		return false
	}
	for i := 0; i < len(signature); i++ {
		if content[offset+i] != signature[i] {
			return false
		}
	}
	return true
}
