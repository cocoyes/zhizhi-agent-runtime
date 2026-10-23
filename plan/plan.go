// Package plan contains the executable plan model and the model-backed
// planning and replanning pipeline.
package plan

import (
	"fmt"
	"sort"
	"strings"
)

type Step struct {
	ID              string         `json:"id" jsonschema:"description=Unique stable step ID"`
	Title           string         `json:"title,omitempty"`
	Description     string         `json:"description,omitempty"`
	Kind            StepKind       `json:"kind,omitempty"`
	Importance      StepImportance `json:"importance,omitempty"`
	Capability      string         `json:"capability" jsonschema:"description=Exact capability from the available tool catalog"`
	DependsOn       []string       `json:"depends_on,omitempty" jsonschema:"description=IDs of steps that must finish before this step"`
	Input           map[string]any `json:"input,omitempty" jsonschema:"description=Static literal input only. Dependency values belong in bindings"`
	Optional        bool           `json:"optional,omitempty"`
	SideEffect      string         `json:"side_effect,omitempty"`
	Idempotency     string         `json:"idempotency,omitempty"`
	Bindings        []InputBinding `json:"bindings,omitempty" jsonschema:"description=Typed mappings from dependency outputs into this step input"`
	Condition       *Condition     `json:"condition,omitempty"`
	Mode            StepMode       `json:"mode,omitempty"`
	Goal            string         `json:"goal,omitempty"`
	SuccessCriteria string         `json:"success_criteria,omitempty"`
	Capabilities    []string       `json:"capabilities,omitempty"`
	ToolBudget      ToolCallBudget `json:"tool_budget,omitempty"`
}

type StepMode string

const (
	StepModeTool    StepMode = "tool"
	StepModeAgentic StepMode = "agentic"
)

type ToolCallBudget struct {
	Min               int `json:"min,omitempty"`
	Max               int `json:"max,omitempty"`
	RequiredSuccesses int `json:"required_successes,omitempty"`
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
	SourceStep string `json:"source_step" jsonschema:"description=ID of a dependency step"`
	SourcePath string `json:"source_path" jsonschema:"description=RFC 6901 path in the source tool output schema"`
	TargetPath string `json:"target_path" jsonschema:"description=RFC 6901 path in the current tool input schema"`
}

// Condition controls whether a step is executed. The referenced step must
// have completed before this condition can be evaluated.
type Condition struct {
	SourceStep string `json:"source_step"`
	SourcePath string `json:"source_path"`
	Equals     any    `json:"equals,omitempty"`
	NotEquals  any    `json:"not_equals,omitempty"`
}

type Plan struct {
	ID      string `json:"id,omitempty"`
	Version int    `json:"version" jsonschema:"description=Integer plan version starting at 1"`
	Goal    string `json:"goal,omitempty"`
	Steps   []Step `json:"steps"`
}

func (p Plan) Validate() error {
	seen := make(map[string]struct{}, len(p.Steps))
	for index, step := range p.Steps {
		if strings.TrimSpace(step.ID) == "" {
			return fmt.Errorf("plan: step id is required")
		}
		if err := validateStepMode(step); err != nil {
			field := "mode"
			if effectiveMode(step.Mode) == StepModeAgentic {
				switch {
				case strings.TrimSpace(step.Goal) == "":
					field = "goal"
				case strings.TrimSpace(step.SuccessCriteria) == "":
					field = "success_criteria"
				case len(step.Capabilities) == 0:
					field = "capabilities"
				default:
					field = "tool_budget"
				}
			} else if strings.TrimSpace(step.Capability) == "" {
				field = "capability"
			}
			return invalidAt("semantic", fmt.Sprintf("/steps/%d/%s", index, field), err)
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
		for _, binding := range step.Bindings {
			for _, source := range p.Steps {
				if source.ID == binding.SourceStep && effectiveMode(source.Mode) == StepModeAgentic {
					return fmt.Errorf("plan: binding cannot reference agentic output")
				}
			}
			if binding.SourceStep == "" || binding.SourcePath == "" || binding.TargetPath == "" {
				return fmt.Errorf("plan: step %q binding requires source_step, source_path, and target_path", step.ID)
			}
			if _, ok := seen[binding.SourceStep]; !ok {
				return fmt.Errorf("plan: step %q binding references unknown step %q", step.ID, binding.SourceStep)
			}
			dependsOnSource := false
			for _, dependency := range step.DependsOn {
				if dependency == binding.SourceStep {
					dependsOnSource = true
					break
				}
			}
			if !dependsOnSource {
				return fmt.Errorf("plan: step %q binding source %q must be a dependency", step.ID, binding.SourceStep)
			}
		}
		if step.Condition != nil {
			for _, source := range p.Steps {
				if source.ID == step.Condition.SourceStep && effectiveMode(source.Mode) == StepModeAgentic {
					return fmt.Errorf("plan: condition cannot reference agentic output")
				}
			}
			if step.Condition.SourceStep == "" {
				return fmt.Errorf("plan: step %q condition source_step is required", step.ID)
			}
			if step.Condition.SourcePath == "" {
				return fmt.Errorf("plan: step %q condition source_path is required", step.ID)
			}
			if (step.Condition.Equals == nil) == (step.Condition.NotEquals == nil) {
				return fmt.Errorf("plan: step %q condition requires exactly one of equals or not_equals", step.ID)
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
		out.Steps[i] = normalizeStep(out.Steps[i])
		out.Steps[i].DependsOn = append([]string(nil), out.Steps[i].DependsOn...)
		sort.Strings(out.Steps[i].DependsOn)
	}
	return out, nil
}

func effectiveMode(mode StepMode) StepMode {
	mode = StepMode(strings.TrimSpace(string(mode)))
	if mode == "" {
		return StepModeTool
	}
	return mode
}

func validateStepMode(step Step) error {
	switch effectiveMode(step.Mode) {
	case StepModeTool:
		if strings.TrimSpace(step.Capability) == "" {
			return fmt.Errorf("plan: tool capability is required")
		}
		if len(step.Capabilities) > 0 || step.ToolBudget != (ToolCallBudget{}) {
			return fmt.Errorf("plan: tool step contains agentic fields")
		}
	case StepModeAgentic:
		if strings.TrimSpace(step.Goal) == "" || strings.TrimSpace(step.SuccessCriteria) == "" || len(step.Capabilities) == 0 {
			return fmt.Errorf("plan: agentic goal, success criteria, and capabilities are required")
		}
		for _, capability := range step.Capabilities {
			if strings.TrimSpace(capability) == "" {
				return fmt.Errorf("plan: agentic capability must be non-empty")
			}
		}
		if strings.TrimSpace(step.Capability) != "" || len(step.Bindings) > 0 {
			return fmt.Errorf("plan: agentic step contains deterministic tool fields")
		}
		b := step.ToolBudget
		if b.Min < 0 || b.Max <= 0 || b.RequiredSuccesses < 0 || b.Min > b.Max || b.RequiredSuccesses > b.Max {
			return fmt.Errorf("plan: invalid finite agentic tool budget")
		}
	default:
		return fmt.Errorf("plan: invalid step mode")
	}
	return nil
}

func normalizeStep(step Step) Step {
	step.Mode = effectiveMode(step.Mode)
	if step.Mode == StepModeAgentic {
		seen := map[string]bool{}
		capabilities := make([]string, 0, len(step.Capabilities))
		for _, capability := range step.Capabilities {
			capability = strings.TrimSpace(capability)
			if !seen[capability] {
				capabilities = append(capabilities, capability)
				seen[capability] = true
			}
		}
		step.Capabilities = capabilities
	}
	return step
}
