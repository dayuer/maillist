package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/store"
)

// maxUploadBytes is upstream MAX_UPLOAD_BYTES: the 4.5 MB a serverless function would take, rounded
// down. It is the operator's own disk here, but an email over a few megabytes is clipped by the
// inbox anyway.
const maxUploadBytes = 4 << 20

// imageType is one of the four formats an email can use, with the signature that has to match.
//
// app/api/uploads/route.js checks the declared content-type against the first bytes. A CLI has no
// content-type, so the signature is what decides the type; the table is the same one.
type imageType struct {
	MediaType string
	Extension string
	Signature string
	// Marker is a second signature at offset 8, which WebP needs: RIFF alone is also WAV and AVI.
	Marker string
}

var imageTypes = []imageType{
	{MediaType: "image/png", Extension: "png", Signature: "\x89PNG"},
	{MediaType: "image/jpeg", Extension: "jpg", Signature: "\xff\xd8\xff"},
	{MediaType: "image/gif", Extension: "gif", Signature: "GIF8"},
	{MediaType: "image/webp", Extension: "webp", Signature: "RIFF", Marker: "WEBP"},
}

func newUploadCmd() *cobra.Command {
	upload := &cobra.Command{
		Use:   "upload <file>",
		Short: "Store an image and print its URL",
		Long: "Writes the file under <data-dir>/uploads/ and prints the public URL, which is what goes " +
			"in a campaign body. The type is decided by the file's first bytes, not by its name: only " +
			"PNG, JPEG, GIF and WebP are accepted, so nothing but a picture can end up next to the " +
			"newsletter's HTML.\n\n" +
			"Images are stored as they are. Upstream's browser-side uploader also scales them down and " +
			"re-encodes large PNGs; that needs a graphics stack, and this is the server.",
		Args: cobra.ExactArgs(1),
		RunE: runUpload,
	}
	return upload
}

func runUpload(cmd *cobra.Command, args []string) error {
	path := args[0]
	content, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the file on the command line
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return fmt.Errorf("%s is empty", path)
	}
	if len(content) > maxUploadBytes {
		return fmt.Errorf("%s is %d bytes; images must be under %d", path, len(content), maxUploadBytes)
	}
	kind, ok := detectImageType(content)
	if !ok {
		return errors.New("this file isn't a valid image: use a PNG, JPEG, GIF or WebP")
	}

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		id := ids.New()
		dir := filepath.Join(a.Config.DataDir, "uploads")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		name := id + "." + kind.Extension
		// Write beside the destination and move it into place: a reader never sees a half-written
		// image, and a failure leaves nothing behind for the URL to resolve to.
		temp, err := os.CreateTemp(dir, name+".*")
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(temp.Name()) }()
		if _, err := temp.Write(content); err != nil {
			_ = temp.Close()
			return err
		}
		if err := temp.Close(); err != nil {
			return err
		}
		// 0640: the server that serves it runs as the same user, and nothing else needs to read it.
		if err := os.Chmod(temp.Name(), 0o640); err != nil { //nolint:gosec // G302: a served image, not a secret
			return err
		}
		if err := os.Rename(temp.Name(), filepath.Join(dir, name)); err != nil {
			return err
		}

		upload := store.Upload{
			ID: id, FileName: filepath.Base(path),
			Slug:      rules.Slugify(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), 60),
			MediaType: kind.MediaType, Extension: kind.Extension,
			Bytes: len(content), CreatedAt: time.Now().UTC(),
		}
		if err := a.Store.CreateUpload(ctx, upload); err != nil {
			// The row is what makes the URL resolvable, so a database failure takes the file with it.
			_ = os.Remove(filepath.Join(dir, name))
			return err
		}
		url := a.Config.AppURL + "/uploads/" + name
		note(cmd, a, "upload.image", upload.FileName, map[string]any{
			"id": upload.ID, "mediaType": upload.MediaType, "bytes": upload.Bytes, "url": url,
		})
		if jsonOutput(cmd) {
			return printJSON(cmd, map[string]any{
				"id": upload.ID, "url": url, "mediaType": upload.MediaType, "bytes": upload.Bytes,
			})
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), url)
		return nil
	})
}

// detectImageType reads the signatures. The declared type is not available here, so the first
// format whose signature — and marker, where the format needs one — matches is the type.
func detectImageType(content []byte) (imageType, bool) {
	for _, kind := range imageTypes {
		if !startsWith(content, kind.Signature, 0) {
			continue
		}
		if kind.Marker != "" && !startsWith(content, kind.Marker, 8) {
			continue
		}
		return kind, true
	}
	return imageType{}, false
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
