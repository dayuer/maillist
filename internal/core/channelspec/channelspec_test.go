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
	if id != "AWS2_ACCESS_KEY_ID" || secret != "AWS2_SECRET_ACCESS_KEY" {
		t.Fatalf("SESKeys = %q, %q", id, secret)
	}
}
