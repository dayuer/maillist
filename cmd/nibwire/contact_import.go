package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/core/importer"
	"nibwire/internal/core/model"
	"nibwire/internal/importsvc"
)

// importSource is what an imported contact's `source` says, and what a suppression written by the
// same import carries as its detail. overview-data.js reads "import" to tell a contact who joined
// before Nibwire (an imported row whose subscribe date is the import time) from one who joined
// here, so the value is load-bearing beyond this command.
const importSource = "import"

// The chunk size and the loop that writes in chunks are internal/importsvc's; this command reads the
// file and reports what the import did.

func newContactImportCmd() *cobra.Command {
	importCmd := &cobra.Command{
		Use:   "import <file.csv>",
		Short: "Import contacts from a CSV export",
		Long: "Reads the columns a provider's export carries (email, status, subscribe date, emails " +
			"received, open rate, click rate — the aliases are core/importer's). A row that names an " +
			"existing contact refreshes its imported engagement history, and a status only ever moves " +
			"stricter: an import can never resurrect somebody who unsubscribed.\n\n" +
			"The file is written in chunks of a thousand, in this process. It does not go through the " +
			"`import_contacts` job: in the container the file is on the operator's machine, and a serve " +
			"process could not open it.",
		Args: cobra.ExactArgs(1),
		RunE: runContactImport,
	}
	flags := importCmd.Flags()
	flags.String("newsletter", "", "newsletter slug or id (required)")
	flags.String("source", importSource, "the source recorded on new contacts")
	flags.String("status-override", "", "use this status for every row instead of the file's")
	flags.Bool("dry-run", false, "report what would happen without writing anything")
	if err := importCmd.MarkFlagRequired("newsletter"); err != nil {
		panic(err)
	}
	return importCmd
}

func runContactImport(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()
	ref, _ := flags.GetString("newsletter")
	source, _ := flags.GetString("source")
	override, _ := flags.GetString("status-override")
	dryRun, _ := flags.GetBool("dry-run")

	var statusOverride model.ContactStatus
	if override != "" {
		parsed, err := parseContactStatus(override)
		if err != nil {
			return err
		}
		statusOverride = parsed
	}

	file, err := os.Open(args[0]) //nolint:gosec // G304: the operator names the file on the command line
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		newsletter, err := newsletterByRef(ctx, a, ref)
		if err != nil {
			return err
		}
		rows, skipped, err := readImportRows(file, statusOverride)
		if err != nil {
			return err
		}
		// Collapse addresses the file lists twice before anything is written (see importer.Dedupe).
		rows = importer.Dedupe(rows)

		result, err := importsvc.Apply(ctx, a.Store, newsletter.ID, source, rows, dryRun, skipped,
			progressTo(cmd.ErrOrStderr()))
		if err != nil {
			return err
		}
		note(cmd, a, "contact.import", args[0], map[string]any{
			"file": args[0], "newsletter": newsletter.Slug, "source": source,
			"created": result.Created, "updated": result.Updated, "suppressed": result.Suppressed,
			"dryRun": result.DryRun,
		})
		if jsonOutput(cmd) {
			return printJSON(cmd, result)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"rows %d  created %d  updated %d  skipped %d  suppressed %d\n",
			result.Rows, result.Created, result.Updated, result.Skipped, result.Suppressed)
		return nil
	})
}

// progressTo adapts the import's progress callback to the stderr line the CLI prints.
func progressTo(out io.Writer) func(done, total int) {
	return func(done, total int) { _, _ = fmt.Fprintf(out, "imported %d/%d\n", done, total) }
}

// readImportRows turns the file into rows the importer understands. The header decides the columns;
// a file with no email column is refused outright, because every other column is optional.
func readImportRows(file io.Reader, statusOverride model.ContactStatus) ([]importer.Row, int, error) {
	reader := csv.NewReader(file)
	// A provider's export can have ragged rows; the missing cells read as empty.
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, 0, errors.New("the file is empty")
		}
		return nil, 0, fmt.Errorf("read the CSV header: %w", err)
	}
	columns := importer.MapColumns(header)
	if columns.Email < 0 {
		return nil, 0, fmt.Errorf("the file has no email column (found %s)", strconv.Quote(header[0]))
	}

	var rows []importer.Row
	skipped := 0
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("read the CSV: %w", err)
		}
		row, ok := importer.NormalizeRow(record, columns, statusOverride)
		if !ok {
			skipped++
			continue
		}
		rows = append(rows, row)
	}
	return rows, skipped, nil
}
