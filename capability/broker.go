package capability

import (
	"fmt"
	"sort"

	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type Candidate struct {
	Capability string
	Tool       tool.Tool
}
type Broker struct{ Registry *tool.Registry }

func NewBroker(registry *tool.Registry) *Broker { return &Broker{Registry: registry} }
func (b *Broker) Resolve(capability string) ([]Candidate, error) {
	if b == nil || b.Registry == nil {
		return nil, fmt.Errorf("capability: registry is required")
	}
	values := b.Registry.ResolveCapability(capability)
	out := make([]Candidate, 0, len(values))
	for _, value := range values {
		out = append(out, Candidate{Capability: capability, Tool: value})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("capability: unresolved %s", capability)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool.Spec().ID < out[j].Tool.Spec().ID })
	return out, nil
}
func (b *Broker) ResolveAll(capabilities []string) (map[string][]Candidate, error) {
	out := make(map[string][]Candidate, len(capabilities))
	for _, value := range capabilities {
		if _, ok := out[value]; ok {
			continue
		}
		candidates, err := b.Resolve(value)
		if err != nil {
			return nil, err
		}
		out[value] = candidates
	}
	return out, nil
}
