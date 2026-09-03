package route

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

type Mode string

const (
	ModeChat    Mode = "chat"
	ModeSimple  Mode = "agent.simple"
	ModeComplex Mode = "agent.complex"
)

type Intent struct {
	Mode         Mode     `json:"mode"`
	Capabilities []string `json:"capabilities,omitempty"`
	Confidence   float64  `json:"confidence"`
	Reason       string   `json:"reason,omitempty"`
}
type Router interface {
	Route(context.Context, string, []model.ToolSpec) (Intent, error)
}

// DefaultRouter is catalog-driven. It never relies on a hand-maintained list
// of natural-language trigger words; complexity comes from distinct catalog matches.
type DefaultRouter struct{ ComplexCapabilityCount int }

func (r DefaultRouter) Route(_ context.Context, input string, tools []model.ToolSpec) (Intent, error) {
	if strings.TrimSpace(input) == "" {
		return Intent{Mode: ModeChat, Confidence: 1, Reason: "empty input"}, nil
	}
	if len(tools) == 0 {
		return Intent{Mode: ModeChat, Confidence: .9, Reason: "tool catalog is empty"}, nil
	}
	hits := map[string]bool{}
	for _, spec := range tools {
		for _, term := range catalogTerms(spec) {
			if len(term) >= 2 && strings.Contains(strings.ToLower(input), term) {
				for _, capability := range specCapabilities(spec) {
					hits[capability] = true
				}
			}
		}
	}
	capabilities := make([]string, 0, len(hits))
	for value := range hits {
		capabilities = append(capabilities, value)
	}
	sort.Strings(capabilities)
	threshold := r.ComplexCapabilityCount
	if threshold < 1 {
		threshold = 2
	}
	if len(capabilities) >= threshold {
		return Intent{Mode: ModeComplex, Capabilities: capabilities, Confidence: .72, Reason: "multiple catalog capabilities matched"}, nil
	}
	if len(capabilities) == 1 {
		return Intent{Mode: ModeSimple, Capabilities: capabilities, Confidence: .78, Reason: "one catalog capability matched"}, nil
	}
	return Intent{Mode: ModeSimple, Confidence: .45, Reason: "tools available; capability match deferred to model"}, nil
}
func specCapabilities(spec model.ToolSpec) []string {
	if len(spec.Capabilities) > 0 {
		return spec.Capabilities
	}
	if spec.Function.Name == "" {
		return nil
	}
	return []string{spec.Function.Name}
}
func catalogTerms(spec model.ToolSpec) []string {
	value := strings.ToLower(spec.Function.Name + " " + spec.Function.Description + " " + strings.Join(spec.Capabilities, " "))
	value = strings.NewReplacer(".", " ", "_", " ", "-", " ", "/", " ").Replace(value)
	return strings.Fields(value)
}

type ModelRouter struct {
	Model    model.Model
	Fallback Router
}

func (r ModelRouter) Route(ctx context.Context, input string, tools []model.ToolSpec) (Intent, error) {
	if r.Model == nil {
		return Intent{}, fmt.Errorf("router: model is required")
	}
	catalog, marshalErr := json.Marshal(model.PlanningToolCatalog(tools))
	if marshalErr != nil {
		return Intent{}, fmt.Errorf("router: encode tool catalog: %w", marshalErr)
	}
	user := input + "\nTool catalog (classification only; do not call):\n" + string(catalog)
	out, err := r.Model.Generate(ctx, model.ModelInput{Messages: []model.Message{{Role: model.RoleSystem, Content: "Classify the request. Return only JSON: {mode: chat|agent.simple|agent.complex, capabilities: string[], confidence: number, reason: string}. Capabilities must come only from the supplied tool catalog."}, {Role: model.RoleUser, Content: user}}, JSONMode: true})
	if err == nil {
		var intent Intent
		if json.Unmarshal([]byte(strings.TrimSpace(out.Text)), &intent) == nil {
			if intent.Mode == ModeChat || intent.Mode == ModeSimple || intent.Mode == ModeComplex {
				allowed := map[string]bool{}
				for _, spec := range tools {
					for _, capability := range specCapabilities(spec) {
						allowed[capability] = true
					}
				}
				filtered := intent.Capabilities[:0]
				for _, capability := range intent.Capabilities {
					if allowed[capability] {
						filtered = append(filtered, capability)
					}
				}
				intent.Capabilities = filtered
				return intent, nil
			}
		}
	}
	if r.Fallback != nil {
		return r.Fallback.Route(ctx, input, tools)
	}
	if err != nil {
		return Intent{}, err
	}
	return Intent{}, fmt.Errorf("router: model returned invalid intent")
}
