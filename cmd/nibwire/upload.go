package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/uploads"
)

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

// runUpload reads the file and hands the bytes to internal/uploads, which is the same code the MCP
// upload_image tool calls: the signature decides the type, and the row plus the file are what make
// the URL resolve.
func runUpload(cmd *cobra.Command, args []string) error {
	path := args[0]
	content, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the file on the command line
	if err != nil {
		return err
	}

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		upload, err := uploads.Save(ctx, a.Store, a.Config.DataDir, path, content)
		if err != nil {
			// The rule is uploads'; the file it was broken on is this command's to name.
			return fmt.Errorf("%s: %w", path, err)
		}
		url := uploads.URL(a.Config.AppURL, upload)
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
