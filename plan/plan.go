package plan

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Step struct {
	ID          string         `json:"id"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Kind        StepKind       `json:"kind,omitempty"`
	Importance  StepImportance `json:"importance,omitempty"`
	Capability  string         `json:"capability"`
	DependsOn   []string       `json:"depends_on,omitempty"`
	Input       map[string]any `json:"input,omitempty"`
	Optional    bool           `json:"optional,omitempty"`
	SideEffect  string         `json:"side_effect,omitempty"`
	Idempotency string         `json:"idempotency,omitempty"`
	Bindings    []InputBinding `json:"bindings,omitempty"`
	Condition   *Condition     `json:"condition,omitempty"`
}

// UnmarshalJSON accepts both the canonical bindings array and the compact
// object form some LLM providers emit, e.g. {"temperature":"weather.temperature"}.
func (s *Step) UnmarshalJSON(data []byte) error {
	type stepFields struct {
		ID          string          `json:"id"`
		Title       string          `json:"title,omitempty"`
		Description string          `json:"description,omitempty"`
		Kind        StepKind        `json:"kind,omitempty"`
		Importance  StepImportance  `json:"importance,omitempty"`
		Capability  string          `json:"capability"`
		DependsOn   []string        `json:"depends_on,omitempty"`
		Input       map[string]any  `json:"input,omitempty"`
		Optional    bool            `json:"optional,omitempty"`
		SideEffect  string          `json:"side_effect,omitempty"`
		Idempotency string          `json:"idempotency,omitempty"`
		Bindings    json.RawMessage `json:"bindings,omitempty"`
		Condition   *Condition      `json:"condition,omitempty"`
	}
	var fields stepFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	bindings, err := parseBindings(fields.Bindings)
	if err != nil {
		return err
	}
	*s = Step{ID: fields.ID, Title: fields.Title, Description: fields.Description, Kind: fields.Kind, Importance: fields.Importance, Capability: fields.Capability, DependsOn: fields.DependsOn, Input: fields.Input, Optional: fields.Optional, SideEffect: fields.SideEffect, Idempotency: fields.Idempotency, Bindings: bindings, Condition: fields.Condition}
	return nil
}

func parseBindings(raw json.RawMessage) ([]InputBinding, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []InputBinding
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if _, hasSource := object["source_step"]; hasSource {
		var binding InputBinding
		if err := json.Unmarshal(raw, &binding); err != nil {
			return nil, err
		}
		return []InputBinding{binding}, nil
	}
	bindings := make([]InputBinding, 0, len(object))
	for target, value := range object {
		var sourcePath string
		if err := json.Unmarshal(value, &sourcePath); err == nil {
			parts := strings.SplitN(strings.TrimPrefix(sourcePath, "."), ".", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("plan: binding %q source %q must use step.path form", target, sourcePath)
			}
			bindings = append(bindings, InputBinding{SourceStep: parts[0], SourcePath: parts[1], TargetPath: target})
			continue
		}
		var binding InputBinding
		if err := json.Unmarshal(value, &binding); err != nil {
			return nil, err
		}
		if binding.TargetPath == "" {
			binding.TargetPath = target
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

type StepKind string

const (
	KindFetch     StepKind = "fetch"
	KindTransform StepKind = "transform"
	KindSynthesis StepKind = "synthesis"
	KindAction    StepKind = "action"
	KindDecision  StepKind = "decision"
)

type StepImportance string

const (
	ImportanceRequired  StepImportance = "required"
	ImportancePreferred StepImportance = "preferred"
	ImportanceOptional  StepImportance = "optional"
)

type InputBinding struct {
	SourceStep string `json:"source_step"`
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
}

// Condition controls whether a step is executed. The referenced step must
// have completed before this condition can be evaluated.
type Condition struct {
	SourceStep string `json:"source_step"`
	SourcePath string `json:"source_path"`
	Equals     any    `json:"equals"`
}

type Plan struct {
	ID      string `json:"id,omitempty"`
	Version int    `json:"version"`
	Goal    string `json:"goal,omitempty"`
	Steps   []Step `json:"steps"`
}

// UnmarshalJSON accepts both numeric and quoted numeric versions. Some
// OpenAI-compatible providers serialize schema integers as strings.
func (p *Plan) UnmarshalJSON(data []byte) error {
	type planFields struct {
		ID      string          `json:"id,omitempty"`
		Version json.RawMessage `json:"version"`
		Goal    string          `json:"goal,omitempty"`
		Steps   []Step          `json:"steps"`
	}
	var fields planFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	version := 0
	if len(fields.Version) > 0 && string(fields.Version) != "null" {
		if err := json.Unmarshal(fields.Version, &version); err != nil {
			var text string
			if stringErr := json.Unmarshal(fields.Version, &text); stringErr != nil {
				return err
			}
			parsed, parseErr := strconv.ParseFloat(text, 64)
			if parseErr != nil || parsed != float64(int(parsed)) {
				return fmt.Errorf("plan: invalid version %q", text)
			}
			version = int(parsed)
		}
	}
	p.ID, p.Version, p.Goal, p.Steps = fields.ID, version, fields.Goal, fields.Steps
	return nil
}

func (p Plan) Validate() error {
	seen := make(map[string]struct{}, len(p.Steps))
	for _, step := range p.Steps {
		if step.ID == "" {
			return fmt.Errorf("plan: step id is required")
		}
		if step.Capability == "" {
			return fmt.Errorf("plan: step %q capability is required", step.ID)
		}
		if _, ok := seen[step.ID]; ok {
			return fmt.Errorf("plan: duplicate step %q", step.ID)
		}
		seen[step.ID] = struct{}{}
	}
	graph := make(map[string][]string, len(p.Steps))
	for _, step := range p.Steps {
		for _, dependency := range step.DependsOn {
			if _, ok := seen[dependency]; !ok {
				return fmt.Errorf("plan: step %q depends on unknown step %q", step.ID, dependency)
			}
			graph[step.ID] = append(graph[step.ID], dependency)
		}
		if step.Condition != nil {
			if step.Condition.SourceStep == "" {
				return fmt.Errorf("plan: step %q condition source_step is required", step.ID)
			}
			if step.Condition.SourcePath == "" {
				return fmt.Errorf("plan: step %q condition source_path is required", step.ID)
			}
			if _, ok := seen[step.Condition.SourceStep]; !ok {
				return fmt.Errorf("plan: step %q condition references unknown step %q", step.ID, step.Condition.SourceStep)
			}
			dependsOnSource := false
			for _, dependency := range step.DependsOn {
				if dependency == step.Condition.SourceStep {
					dependsOnSource = true
					break
				}
			}
			if !dependsOnSource {
				return fmt.Errorf("plan: step %q condition source %q must be a dependency", step.ID, step.Condition.SourceStep)
			}
		}
	}
	state := make(map[string]uint8, len(graph))
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("plan: dependency cycle at %q", id)
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, dependency := range graph[id] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range seen {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// Normalize returns a deterministic copy suitable for traces and compilation.
func (p Plan) Normalize() (Plan, error) {
	if err := p.Validate(); err != nil {
		return Plan{}, err
	}
	out := p
	out.Steps = append([]Step(nil), p.Steps...)
	sort.SliceStable(out.Steps, func(i, j int) bool { return out.Steps[i].ID < out.Steps[j].ID })
	for i := range out.Steps {
		out.Steps[i].DependsOn = append([]string(nil), out.Steps[i].DependsOn...)
		sort.Strings(out.Steps[i].DependsOn)
	}
	return out, nil
}
