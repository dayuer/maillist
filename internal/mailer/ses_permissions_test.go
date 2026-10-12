package mailer

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// sesCall matches one call to the SES v2 client. It is deliberately about the receiver rather than
// about a list of operation names: a list would have to be edited by whoever adds the call, which is
// exactly the moment this test exists to not rely on.
var sesCall = regexp.MustCompile(`s\.client\.([A-Z][A-Za-z0-9]*)\(`)

// 代码调用的 SES 操作必须都在文档列出的权限里。
//
// A permission that is missing from the policy does not fail loudly at deploy time: SES answers the
// call with an authorization error at the moment it is needed, which for two of the four is a
// "cannot see the domains" or a silent one email a second. So the documentation is pinned to the
// code here rather than trusted to stay in step.
func TestTheDocumentedPermissionsCoverEverySESCall(t *testing.T) {
	called := sesOperationsCalled(t)
	// A canary for the scan itself: if the pattern stops matching, the test would otherwise pass by
	// finding nothing to check.
	if len(called) < 4 {
		t.Fatalf("found only %v: the scan has stopped seeing the calls it exists to check", called)
	}

	documented := readFile(t, filepath.Join("..", "..", "docs", "ses-smoke.md"))
	for _, operation := range called {
		if !strings.Contains(documented, "ses:"+operation) {
			t.Errorf("the code calls %s and docs/ses-smoke.md does not list ses:%s", operation, operation)
		}
	}
	// And the file the operator actually edits points at that list rather than repeating it.
	envExample := readFile(t, filepath.Join("..", "..", "deploy", ".env.example"))
	if !strings.Contains(envExample, "docs/ses-smoke.md") {
		t.Error("deploy/.env.example does not point at the IAM policy in docs/ses-smoke.md")
	}
}

// sesOperationsCalled is which SES v2 operations this package asks for, read from its own source.
func sesOperationsCalled(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		for _, match := range sesCall.FindAllStringSubmatch(readFile(t, file), -1) {
			seen[match[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for operation := range seen {
		out = append(out, operation)
	}
	sort.Strings(out)
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	// The paths here are the package's own source and two files of this repository, named in the test.
	content, err := os.ReadFile(path) //nolint:gosec // G304: a repository path, not user input
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
