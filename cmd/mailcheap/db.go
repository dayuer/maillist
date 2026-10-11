package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/store"
)

// newDBCmd is the operator's handle on the file itself. `init` creates a database and `serve`
// migrates it at startup; these are for the two things that outlive both: a copy, and a restore.
func newDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Work on the database file",
	}

	migrate := &cobra.Command{
		Use:   "migrate",
		Short: "Apply the migrations and report what was applied",
		Long: "Opening the database migrates it, so this is what `serve` already does at startup. Run " +
			"it to migrate without serving, or to see which migrations this binary knows.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				applied, err := a.Store.AppliedMigrations(ctx)
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, applied)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %d migration(s) applied\n",
					a.Store.Path(), len(applied))
				for _, version := range applied {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", version)
				}
				return nil
			})
		},
	}

	backup := &cobra.Command{
		Use:   "backup --to <path>",
		Short: "Write a consistent copy of the database",
		Long: "SQLite takes the copy itself (VACUUM INTO), so it is one consistent snapshot taken while " +
			"the database keeps serving — not a file copy that could catch a checkpoint halfway. The " +
			"destination must not exist.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			to, _ := cmd.Flags().GetString("to")
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				if err := a.Store.BackupTo(ctx, to); err != nil {
					return err
				}
				info, err := os.Stat(to)
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, map[string]any{"path": to, "bytes": info.Size()})
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "backed up to %s (%d bytes)\n", to, info.Size())
				return nil
			})
		},
	}
	backup.Flags().String("to", "", "where to write the copy (required)")

	restore := &cobra.Command{
		Use:   "restore --from <path>",
		Short: "Replace the database with a copy",
		Long: "Destructive, and it asks first. The current database is copied to " +
			"<db>.bak-<timestamp> before anything is replaced, the copy is checked by opening it, the " +
			"stale -wal and -shm files are removed (leaving them would replay an old log over the " +
			"restored file), and the swap is a rename in the same directory.\n\n" +
			"Stop the server before restoring: a process holding the old file keeps writing to it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			from, _ := cmd.Flags().GetString("from")
			if from == "" {
				return fmt.Errorf("--from is required")
			}
			yes, _ := cmd.Flags().GetBool("yes")
			return restoreDatabase(cmd, from, yes)
		},
	}
	restore.Flags().String("from", "", "the copy to restore (required)")

	cmd.AddCommand(migrate, backup, restore)
	return cmd
}

// restoreDatabase is the whole of `db restore`, in the order that matters.
func restoreDatabase(cmd *cobra.Command, from string, yes bool) error {
	cfg, err := configFromFlags(cmd)
	if err != nil {
		return err
	}
	info, err := os.Stat(from)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Size() == 0 {
		return fmt.Errorf("%s is not a database file", from)
	}
	// The copy has to be a database this binary can open and query, or the restore replaces a working
	// file with a broken one. Opening it also migrates it, which is what a restore of an older
	// backup wants anyway.
	source, err := store.Open(cmd.Context(), from)
	if err != nil {
		return fmt.Errorf("%s is not a database this binary can open: %w", from, err)
	}
	if err := source.Ping(cmd.Context()); err != nil {
		_ = source.Close()
		return fmt.Errorf("%s does not answer a query: %w", from, err)
	}
	if err := source.Close(); err != nil {
		return err
	}

	if !yes && !confirm(cmd) {
		return fmt.Errorf("aborted")
	}

	// The way back, before anything is touched.
	backup := fmt.Sprintf("%s.bak-%s", cfg.DBPath, time.Now().UTC().Format("20060102T150405"))
	if _, err := os.Stat(cfg.DBPath); err == nil {
		if err := copyFile(cfg.DBPath, backup); err != nil {
			return err
		}
	}
	// The restored file is a fresh database; a -wal from the old one would be replayed over it.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(cfg.DBPath + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	temp := cfg.DBPath + ".restore"
	if err := copyFile(from, temp); err != nil {
		return err
	}
	if err := os.Rename(temp, cfg.DBPath); err != nil {
		return err
	}

	restored, err := store.Open(cmd.Context(), cfg.DBPath)
	if err != nil {
		return err
	}
	if err := restored.Close(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "restored %s from %s (the previous file is at %s)\n",
		cfg.DBPath, from, backup)
	return nil
}

// copyFile copies one file, creating or truncating the destination.
func copyFile(from, to string) error {
	content, err := os.ReadFile(from) //nolint:gosec // G304: both paths come from the command line
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
		return err
	}
	// The operator names both paths; there is nothing to traverse from inside a local file.
	return os.WriteFile(to, content, 0o600) //nolint:gosec // G703: a path from this command's own flags
}
