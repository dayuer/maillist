// Package channelspec says whether a configured channel is one this release can build: its name,
// the prefix of its secret variables, its type and that type's settings. It is pure — the caller
// passes the environment lookup — so the CLI can refuse a bad channel before it is stored and the
// server can refuse to start with one.
package channelspec

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"nibwire/internal/core/model"
)

// TypeSES is a channel backed by an Amazon SES account.
const TypeSES = "ses"

// Spec is what a channel is configured with.
type Spec struct {
	Name         string
	Type         string
	SecretEnv    string
	Params       map[string]string
	MaxPerSecond int
	MaxPerDay    int
}

var (
	namePattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	secretEnvPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	// tenantPattern is SES's rule for a tenant name, as config.go applies it to SES_TENANT_NAME.
	tenantPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// sesParams are the settings an ses channel accepts.
	sesParams = map[string]bool{"region": true, "configuration_set": true, "tenant": true, "endpoint": true}
	// laterTypes are designed but not built yet.
	laterTypes = map[string]bool{"resend": true, "sendgrid": true}
)

// SESKeys are the variables an ses channel reads its access key from.
func SESKeys(prefix string) (accessKeyID, secretAccessKey string) {
	return prefix + "_ACCESS_KEY_ID", prefix + "_SECRET_ACCESS_KEY"
}

// Validate reports every problem with s at once. lookup reads the environment.
func Validate(s Spec, lookup func(string) string) error {
	var problems []string
	switch {
	case s.Name == model.DefaultChannel:
		problems = append(problems,
			`"default" is the channel the SES_* settings describe; pick another name`)
	case !namePattern.MatchString(s.Name):
		problems = append(problems, fmt.Sprintf("name %q: lowercase letters, digits and hyphens, "+
			"starting with a letter, at most 32", s.Name))
	}
	secretOK := secretEnvPattern.MatchString(s.SecretEnv)
	if !secretOK {
		problems = append(problems, fmt.Sprintf("secret env %q: uppercase letters, digits and "+
			"underscores, starting with a letter, at most 64", s.SecretEnv))
	}
	if s.MaxPerSecond < 0 || s.MaxPerDay < 0 {
		problems = append(problems, "the limits cannot be negative")
	}
	switch {
	case s.Type == TypeSES:
		problems = append(problems, sesProblems(s, lookup, secretOK)...)
	case laterTypes[s.Type]:
		problems = append(problems, fmt.Sprintf("type %s is not available in this release yet", s.Type))
	default:
		problems = append(problems, fmt.Sprintf("type %q: must be ses", s.Type))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func sesProblems(s Spec, lookup func(string) string, secretOK bool) []string {
	var problems []string
	keys := make([]string, 0, len(s.Params))
	for key := range s.Params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !sesParams[key] {
			problems = append(problems, fmt.Sprintf("an ses channel has no %q setting", key))
		}
	}
	if s.Params["region"] == "" && s.Params["endpoint"] == "" {
		problems = append(problems, "an ses channel needs --region")
	}
	if tenant := s.Params["tenant"]; tenant != "" && !tenantPattern.MatchString(tenant) {
		problems = append(problems, fmt.Sprintf("tenant %q: letters, digits, hyphens and "+
			"underscores, at most 64", tenant))
	}
	if secretOK {
		id, secret := SESKeys(s.SecretEnv)
		if (lookup(id) == "") != (lookup(secret) == "") {
			problems = append(problems, fmt.Sprintf("set both %s and %s, or neither to use the "+
				"instance role", id, secret))
		}
	}
	return problems
}
