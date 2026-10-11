package main

import (
	"fmt"

	"github.com/spf13/cobra"
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
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
			return err
		},
	})
	return root
}
