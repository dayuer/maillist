package main

import (
	"bytes"
	"testing"
)

func TestVersionPrintsBuildVersion(t *testing.T) {
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "dev\n" {
		t.Fatalf("got %q", out.String())
	}
}
