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

	"mailcheap/internal/app"
	"mailcheap/internal/core/importer"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/store"
)

// importSource is what an imported contact's `source` says, and what a suppression written by the
// same import carries as its detail. overview-data.js reads "import" to tell a contact who joined
// before Mailcheap (an imported row whose subscribe date is the import time) from one who joined
// here, so the value is load-bearing beyond this command.
const importSource = "import"

// importChunkSize is one transaction's worth of rows. Small enough that a killed import leaves most
// of its work committed, large enough that 100k rows are a hundred transactions rather than a
// hundred thousand (design §5.2).
const importChunkSize = 1000

// importResult is what the command reports and what `--json` prints.
type importResult struct {
	Rows       int  `json:"rows"`
	Created    int  `json:"created"`
	Updated    int  `json:"updated"`
	Skipped    int  `json:"skipped"`
	Suppressed int  `json:"suppressed"`
	DryRun     bool `json:"dryRun,omitempty"`
}

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

		result, err := applyImport(ctx, a, newsletter, rows, source, dryRun, skipped, cmd.ErrOrStderr())
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

// applyImport writes the rows in chunks.
//
// The contact map is read once and reused: Dedupe already collapsed repeated addresses, so a later
// chunk can never name a contact an earlier chunk created, and the map can only be wrong about rows
// this import has not reached yet. Re-reading it per chunk would make a 100k-row import quadratic.
func applyImport(ctx context.Context, a *app.App, newsletter model.Newsletter, rows []importer.Row,
	source string, dryRun bool, skipped int, progress io.Writer) (importResult, error) {
	result := importResult{Rows: len(rows), Skipped: skipped, DryRun: dryRun}
	existing, err := contactMap(ctx, a, newsletter.ID)
	if err != nil {
		return result, err
	}

	for _, chunk := range rules.Chunk(rows, importChunkSize) {
		// Merge is pure and cheap; running it here is what makes the suppression count and the dry
		// run possible without a second contract with the store.
		for _, write := range importer.Merge(chunk, existing) {
			if write.Suppress != "" {
				result.Suppressed++
			}
		}
		if dryRun {
			created, updated := countMerge(chunk, existing)
			result.Created += created
			result.Updated += updated
		} else {
			created, updated, err := a.Store.ApplyImportRows(ctx, newsletter.ID, source, chunk, existing)
			if err != nil {
				return result, err
			}
			result.Created += created
			result.Updated += updated
		}
		if progress != nil {
			_, _ = fmt.Fprintf(progress, "imported %d/%d\n", result.Created+result.Updated, result.Rows)
		}
	}
	return result, nil
}

// countMerge is the dry run's arithmetic: the same decisions, none of the writes.
func countMerge(rows []importer.Row, existing map[string]model.Contact) (created, updated int) {
	for _, write := range importer.Merge(rows, existing) {
		switch {
		case write.Insert != nil:
			created++
		case write.UpdateID != "":
			updated++
		}
	}
	return created, updated
}

// contactMap is every contact of the newsletter by address, which is what Merge looks rows up in.
func contactMap(ctx context.Context, a *app.App, newsletterID string) (map[string]model.Contact, error) {
	total, err := a.Store.CountContacts(ctx, store.ContactFilter{NewsletterID: newsletterID})
	if err != nil {
		return nil, err
	}
	contacts, err := a.Store.Contacts(ctx, store.ContactFilter{NewsletterID: newsletterID, Limit: total})
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.Contact, len(contacts))
	for _, contact := range contacts {
		out[contact.Email] = contact
	}
	return out, nil
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
