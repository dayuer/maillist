package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fail(root, err)
	}
}

// fail reports a failed run in the shape the caller asked for, and exits 1 either way.
//
// --json exists so a script can read this tool's output as JSON; an error that then arrives as bare
// text breaks that contract at exactly the moment the script is looking for a reason. The error goes
// to stderr in both shapes, so stdout stays whatever the command managed to produce, and the exit
// status is the 1 a caller already checks.
func fail(root *cobra.Command, err error) {
	// The persistent set is where the flag is defined, and its *pflag.Flag is the one the executed
	// subcommand parsed into, so this reads the value the caller actually passed whichever command
	// failed.
	jsonOn, _ := root.PersistentFlags().GetBool("json")
	if jsonOn {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": err.Error()})
	} else {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(1)
}
