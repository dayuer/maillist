package channelspec

import (
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func valid() Spec {
	return Spec{Name: "aws2", Type: TypeSES, SecretEnv: "AWS2", Params: map[string]string{"region": "us-east-1"}}
}

func TestAValidSESChannel(t *testing.T) {
	for name, lookup := range map[string]func(string) string{
		"keys":          env(map[string]string{"AWS2_ACCESS_KEY_ID": "AKIA", "AWS2_SECRET_ACCESS_KEY": "s"}),
		"instance role": env(nil),
	} {
		if err := Validate(valid(), lookup); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidateRefuses(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Spec)
		lookup func(string) string
		want   string
	}{
		{"the default name", func(s *Spec) { s.Name = "default" }, env(nil), "SES_* settings"},
		{"an uppercase name", func(s *Spec) { s.Name = "AWS2" }, env(nil), "name"},
		{"a name starting with a digit", func(s *Spec) { s.Name = "2aws" }, env(nil), "name"},
		{"a 33-character name", func(s *Spec) { s.Name = "a" + strings.Repeat("b", 32) }, env(nil), "name"},
		{"a lowercase secret prefix", func(s *Spec) { s.SecretEnv = "aws2" }, env(nil), "secret env"},
		{"a negative limit", func(s *Spec) { s.MaxPerDay = -1 }, env(nil), "negative"},
		{"an unknown type", func(s *Spec) { s.Type = "smtp" }, env(nil), "must be ses"},
		{"resend before its release", func(s *Spec) { s.Type = "resend" }, env(nil), "not available"},
		{"sendgrid before its release", func(s *Spec) { s.Type = "sendgrid" }, env(nil), "not available"},
		{"ses without region or endpoint", func(s *Spec) { s.Params = map[string]string{} }, env(nil), "--region"},
		{"an unknown ses setting", func(s *Spec) { s.Params["colour"] = "red" }, env(nil), `"colour"`},
		{"a bad tenant", func(s *Spec) { s.Params["tenant"] = "no spaces" }, env(nil), "tenant"},
		{"only the key id", func(*Spec) {}, env(map[string]string{"AWS2_ACCESS_KEY_ID": "AKIA"}), "both"},
		{"only the secret", func(*Spec) {}, env(map[string]string{"AWS2_SECRET_ACCESS_KEY": "s"}), "both"},
	}
	for _, c := range cases {
		spec := valid()
		c.change(&spec)
		err := Validate(spec, c.lookup)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

// Every problem is reported at once, the way config.Load does.
func TestValidateReportsEveryProblem(t *testing.T) {
	spec := Spec{Name: "Bad", Type: TypeSES, SecretEnv: "lower"}
	err := Validate(spec, env(nil))
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"name", "secret env", "--region"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not mention %q", err, want)
		}
	}
}

func TestSESKeys(t *testing.T) {
	id, secret := SESKeys("AWS2")
	if id != "AWS2_ACCESS_KEY_ID" || secret != "AWS2_SECRET_ACCESS_KEY" { //nolint:gosec // G101: variable names, not credentials
		t.Fatalf("SESKeys = %q, %q", id, secret)
	}
}

func TestSecretVarsNamesTheVariablesOfAType(t *testing.T) {
	got := SecretVars(TypeSES, "AWS2")
	if len(got) != 2 || got[0] != "AWS2_ACCESS_KEY_ID" || got[1] != "AWS2_SECRET_ACCESS_KEY" {
		t.Fatalf("SecretVars(ses) = %v", got)
	}
	if got := SecretVars("resend", "R"); got != nil {
		t.Fatalf("SecretVars(resend) = %v, want none until the type exists", got)
	}
}

func TestValidateFallbacks(t *testing.T) {
	if err := ValidateFallbacks("aws2", []string{"default", "resend"}); err != nil {
		t.Fatalf("a good chain: %v", err)
	}
	if err := ValidateFallbacks("default", []string{"aws2"}); err != nil {
		t.Fatalf("default as the source: %v", err)
	}
	for _, c := range []struct {
		chain []string
		want  string
	}{
		{nil, "empty"},
		{[]string{"default", ""}, "empty name"},
		{[]string{"default", "aws2"}, "aws2 cannot fall back to itself"},
		{[]string{"default", "back", "default"}, "default appears twice"},
	} {
		err := ValidateFallbacks("aws2", c.chain)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("ValidateFallbacks(aws2, %v) = %v, want %q", c.chain, err, c.want)
		}
	}
}

func TestWarningsNameTheMissingSecrets(t *testing.T) {
	both := env(map[string]string{"AWS2_ACCESS_KEY_ID": "AKIA", "AWS2_SECRET_ACCESS_KEY": "s"})
	if got := Warnings(valid(), both); len(got) != 0 {
		t.Fatalf("a channel with both keys warned: %v", got)
	}
	half := Warnings(valid(), env(map[string]string{"AWS2_ACCESS_KEY_ID": "AKIA"}))
	if len(half) != 1 || !strings.Contains(half[0], "AWS2_SECRET_ACCESS_KEY") {
		t.Fatalf("half the keys: %v", half)
	}
	none := Warnings(valid(), env(nil))
	if len(none) != 1 || !strings.Contains(none[0], "AWS2_ACCESS_KEY_ID and AWS2_SECRET_ACCESS_KEY are not set") ||
		!strings.Contains(none[0], "instance role") {
		t.Fatalf("no keys: %v", none)
	}
}
