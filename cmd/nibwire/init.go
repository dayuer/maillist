package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"nibwire/internal/store"
)

// newInitCmd creates the data directory and the database, and generates SESSION_SECRET when the
// environment has none. It is the one command that does not need a secret already.
func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the database and, if needed, a SESSION_SECRET",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir, dbPath := initPaths(cmd)
			if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
				return err
			}
			if err := os.MkdirAll(dataDir, 0o750); err != nil {
				return err
			}
			if os.Getenv("SESSION_SECRET") == "" {
				secret, err := randomSecret()
				if err != nil {
					return err
				}
				envPath := filepath.Join(dataDir, "nibwire.env")
				if err := os.WriteFile(envPath, []byte("SESSION_SECRET="+secret+"\n"), 0o600); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(),
					"wrote %s — export it, or pass SESSION_SECRET in the environment\n", envPath)
			}

			db, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), dbPath)
			return nil
		},
	}
}

// initPaths works out where the database and the generated secret go. It cannot use config.Load,
// which refuses to load without the very SESSION_SECRET this command is there to create.
func initPaths(cmd *cobra.Command) (dataDir, dbPath string) {
	dataDir, _ = cmd.Flags().GetString("data-dir")
	if dataDir == "" {
		dataDir = os.Getenv("NIBWIRE_DATA_DIR")
	}
	dbPath, _ = cmd.Flags().GetString("db")
	if dbPath == "" {
		dbPath = os.Getenv("NIBWIRE_DB")
	}
	if dbPath != "" && dataDir == "" {
		dataDir = filepath.Dir(dbPath)
	}
	if dataDir == "" {
		dataDir = "/data"
	}
	if dbPath == "" {
		dbPath = filepath.Join(dataDir, "nibwire.db")
	}
	return dataDir, dbPath
}

// randomSecret is what `openssl rand -base64 48` gives: 48 random bytes, base64.
func randomSecret() (string, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
