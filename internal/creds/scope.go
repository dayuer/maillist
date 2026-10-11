package creds

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Scope is what a credential may do (design §11, layer 2).
type Scope string

const (
	// ScopeRead covers every list, get, stats, report, check and count.
	ScopeRead Scope = "read"
	// ScopeWrite covers newsletters, campaign drafts, contacts, suppressions, cleanup and uploads.
	ScopeWrite Scope = "write"
	// ScopeSend covers test, send, schedule, pause, resume and import — anything that can put mail in
	// motion.
	ScopeSend Scope = "send"
)

// AllScopes is every scope, in the order they print and store.
var AllScopes = []Scope{ScopeRead, ScopeWrite, ScopeSend}

// ParseScopes validates an operator's `--scopes read,write` and returns them deduplicated in
// AllScopes order, so what is stored does not depend on how it was typed.
func ParseScopes(values []string) ([]Scope, error) {
	wanted := map[Scope]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part == "" {
				continue
			}
			scope := Scope(part)
			if !knownScope(scope) {
				return nil, fmt.Errorf("unknown scope %q: read, write, send", part)
			}
			wanted[scope] = true
		}
	}
	out := make([]Scope, 0, len(wanted))
	for _, scope := range AllScopes {
		if wanted[scope] {
			out = append(out, scope)
		}
	}
	return out, nil
}

// Allows reports whether scopes contains want. A credential with no scopes allows nothing: the
// absence of a list is not an invitation.
func Allows(scopes []Scope, want Scope) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

// Encode is the JSON array the credentials.scopes column holds.
func Encode(scopes []Scope) (string, error) {
	if len(scopes) == 0 {
		return "[]", nil
	}
	encoded, err := json.Marshal(scopes)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// Decode reads the column back. A value that is not a JSON array is an error rather than an empty
// scope set: a credential whose scopes cannot be read must not be quietly harmless.
func Decode(value string) ([]Scope, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var raw []string
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, fmt.Errorf("credential scopes %q: %w", value, err)
	}
	out := make([]Scope, 0, len(raw))
	for _, item := range raw {
		scope := Scope(item)
		if !knownScope(scope) {
			return nil, fmt.Errorf("credential has an unknown scope %q", item)
		}
		out = append(out, scope)
	}
	return out, nil
}

func knownScope(scope Scope) bool {
	for _, known := range AllScopes {
		if scope == known {
			return true
		}
	}
	return false
}
