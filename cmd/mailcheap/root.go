package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/config"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "mailcheap",
		Short:         "Self-hosted newsletters on Amazon SES",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().Bool("json", false, "print structured JSON output")
	root.PersistentFlags().Bool("yes", false, "skip confirmation prompts")
	root.PersistentFlags().String("db", "", "path to the SQLite database (overrides MAILCHEAP_DB)")
	root.PersistentFlags().String("data-dir", "", "directory holding the database (overrides MAILCHEAP_DATA_DIR)")
	root.AddCommand(
		versionCmd(),
		newInitCmd(),
		newServeCmd(),
		newNewsletterCmd(),
		newContactCmd(),
		newCampaignCmd(),
		newSuppressionCmd(),
		newStatsCmd(),
	)
	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
			return err
		},
	}
}

// withApp loads the configuration from the environment (plus the two path flags), opens the App and
// runs fn. Every data-touching command goes through it, so the database is closed exactly once and
// the flags mean the same thing everywhere.
func withApp(cmd *cobra.Command, run func(ctx context.Context, a *app.App) error) error {
	cfg, err := configFromFlags(cmd)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	a, err := app.New(cmd.Context(), cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	return run(cmd.Context(), a)
}

// configFromFlags is config.Load with the --db and --data-dir overrides applied.
func configFromFlags(cmd *cobra.Command) (config.Config, error) {
	cfg, err := config.Load(nil)
	if err != nil {
		return config.Config{}, err
	}
	if dataDir, _ := cmd.Flags().GetString("data-dir"); dataDir != "" {
		cfg.DataDir = dataDir
		cfg.DBPath = dataDir + "/mailcheap.db"
	}
	if dbPath, _ := cmd.Flags().GetString("db"); dbPath != "" {
		cfg.DBPath = dbPath
	}
	return cfg, nil
}

// jsonOutput reports whether the command was asked for structured output.
func jsonOutput(cmd *cobra.Command) bool {
	on, _ := cmd.Flags().GetBool("json")
	return on
}

// printJSON writes one indented JSON value.
func printJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// confirm asks on stderr and reads a yes from stdin. Anything but y/yes is a no.
func confirm(cmd *cobra.Command) bool {
	_, _ = fmt.Fprint(cmd.ErrOrStderr(), "Continue? [y/N] ")
	var answer string
	_, err := fmt.Fscanln(cmd.InOrStdin(), &answer)
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
