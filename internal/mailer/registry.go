package mailer

import (
	"fmt"
	"sort"

	"nibwire/internal/core/model"
)

// Registry is every channel a deployment can send through, by name: the default channel (the
// SES_* settings) and the configured ones.
type Registry struct {
	byName map[string]Channel
	order  []Channel // default first, then by name
}

// NewRegistry builds a registry. The first channel must be the default one; names must be unique.
func NewRegistry(defaultChannel Channel, others ...Channel) (*Registry, error) {
	if defaultChannel.Name() != model.DefaultChannel {
		return nil, fmt.Errorf("the first channel must be %q, not %q", model.DefaultChannel,
			defaultChannel.Name())
	}
	registry := &Registry{byName: map[string]Channel{model.DefaultChannel: defaultChannel}}
	sorted := append([]Channel(nil), others...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name() < sorted[j].Name() })
	for _, channel := range sorted {
		if _, taken := registry.byName[channel.Name()]; taken {
			return nil, fmt.Errorf("channel %q is configured twice", channel.Name())
		}
		registry.byName[channel.Name()] = channel
	}
	registry.order = append([]Channel{defaultChannel}, sorted...)
	return registry, nil
}

// Get finds a channel by name.
func (r *Registry) Get(name string) (Channel, bool) {
	channel, ok := r.byName[name]
	return channel, ok
}

// Default is the channel the SES_* settings describe.
func (r *Registry) Default() Channel { return r.order[0] }

// All lists every channel, default first, then by name.
func (r *Registry) All() []Channel { return append([]Channel(nil), r.order...) }
