package mailer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// `nibwire campaign test --json` prints a TestResult as is, so its keys are a CLI contract: they
// keep the names they had before sending went through channels.
func TestTheTestResultJSONKeepsItsKeys(t *testing.T) {
	out, err := json.Marshal(TestResult{MessageID: "m", ProviderMessageID: "p", SentAt: time.Unix(0, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, key := range []string{`"MessageID":`, `"SESMessageID":`, `"SentAt":`} {
		if !strings.Contains(got, key) {
			t.Errorf("TestResult JSON %s lacks %s", got, key)
		}
	}
	if strings.Contains(got, "ProviderMessageID") {
		t.Errorf("TestResult JSON %s says ProviderMessageID; the CLI key is SESMessageID", got)
	}
}
