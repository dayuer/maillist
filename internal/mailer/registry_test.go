package mailer

import (
	"strings"
	"testing"

	"nibwire/internal/core/model"
)

func testChannel(name string) Channel {
	return NewChannel(name, ChannelTypeSES, NewFake(), StaticQuota{}, StaticIdentity{}, ClassifySES)
}

func TestRegistryFindsEachChannel(t *testing.T) {
	registry, err := NewRegistry(testChannel(model.DefaultChannel), testChannel("zeta"), testChannel("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if registry.Default().Name() != model.DefaultChannel {
		t.Fatalf("Default = %s", registry.Default().Name())
	}
	for _, name := range []string{model.DefaultChannel, "alpha", "zeta"} {
		channel, ok := registry.Get(name)
		if !ok || channel.Name() != name {
			t.Errorf("Get(%s) = %v, %v", name, channel, ok)
		}
	}
	if _, ok := registry.Get("missing"); ok {
		t.Error("Get(missing) found something")
	}
	var names []string
	for _, channel := range registry.All() {
		names = append(names, channel.Name())
	}
	if got := strings.Join(names, ","); got != "default,alpha,zeta" {
		t.Fatalf("All = %s, want default first, then by name", got)
	}
}

func TestRegistryRefusesBadSets(t *testing.T) {
	if _, err := NewRegistry(testChannel("other")); err == nil ||
		!strings.Contains(err.Error(), "default") {
		t.Errorf("a registry whose first channel is not default: err = %v", err)
	}
	if _, err := NewRegistry(testChannel(model.DefaultChannel), testChannel("a"), testChannel("a")); err == nil ||
		!strings.Contains(err.Error(), "twice") {
		t.Errorf("a duplicate name: err = %v", err)
	}
	if _, err := NewRegistry(testChannel(model.DefaultChannel), testChannel(model.DefaultChannel)); err == nil {
		t.Error("a second default was accepted")
	}
}
