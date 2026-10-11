package creds

import (
	"reflect"
	"testing"
)

func TestParseScopes(t *testing.T) {
	cases := []struct {
		name    string
		values  []string
		want    []Scope
		wantErr bool
	}{
		{"one", []string{"read"}, []Scope{ScopeRead}, false},
		{"comma separated", []string{"read,write"}, []Scope{ScopeRead, ScopeWrite}, false},
		{"repeated flags", []string{"read", "write"}, []Scope{ScopeRead, ScopeWrite}, false},
		{"spaces and case", []string{" WRITE , send "}, []Scope{ScopeWrite, ScopeSend}, false},
		// The stored order is the declared order, not the typed one: what a credential may do cannot
		// depend on how the operator wrote it.
		{"order is normalised", []string{"send,read,write"}, AllScopes, false},
		{"duplicates collapse", []string{"read,read"}, []Scope{ScopeRead}, false},
		{"empty entries are ignored", []string{"read,,write,"}, []Scope{ScopeRead, ScopeWrite}, false},
		{"nothing at all", nil, nil, false},
		{"unknown", []string{"read,admin"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseScopes(tc.values)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseScopes(%v) = %v, want an error", tc.values, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestAllows(t *testing.T) {
	if Allows(nil, ScopeRead) {
		t.Error("a credential with no scopes allowed something")
	}
	if Allows([]Scope{ScopeRead}, ScopeSend) {
		t.Error("read allowed send")
	}
	if !Allows([]Scope{ScopeRead, ScopeSend}, ScopeSend) {
		t.Error("send was refused to a credential that has it")
	}
}

func TestEncodeDecode(t *testing.T) {
	encoded, err := Encode([]Scope{ScopeRead, ScopeSend})
	if err != nil {
		t.Fatal(err)
	}
	if encoded != `["read","send"]` {
		t.Fatalf("encoded = %q", encoded)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, []Scope{ScopeRead, ScopeSend}) {
		t.Fatalf("decoded = %v", decoded)
	}

	// An empty column is no scopes, not an error: it is the column's default.
	if scopes, err := Decode(""); err != nil || scopes != nil {
		t.Fatalf("Decode(\"\") = %v %v", scopes, err)
	}
	// But something that is not a JSON array is an error: a credential whose scopes cannot be read
	// must not be quietly powerless — or quietly powerful.
	if _, err := Decode("read,write"); err == nil {
		t.Error("a non-JSON scope column was accepted")
	}
	if _, err := Decode(`["read","admin"]`); err == nil {
		t.Error("an unknown scope in the column was accepted")
	}
}
