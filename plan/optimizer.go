package plan

import (
	"encoding/json"
	"fmt"
)

// Optimize applies conservative, semantics-preserving rewrites. It only
// merges exact duplicate read steps; it never merges writes or steps with
// different explicit inputs/dependencies.
func Optimize(source Plan) (Plan, error) {
	normalized, err := source.Normalize()
	if err != nil {
		return Plan{}, err
	}
	canonical := map[string]string{}
	aliases := map[string]string{}
	out := make([]Step, 0, len(normalized.Steps))
	for _, step := range normalized.Steps {
		if step.SideEffect != "" && step.SideEffect != "none" && step.SideEffect != "read" {
			out = append(out, step)
			continue
		}
		keyBytes, _ := json.Marshal(struct {
			Capability string
			Input      map[string]any
			Depends    []string
			Condition  *Condition
		}{step.Capability, step.Input, step.DependsOn, step.Condition})
		key := string(keyBytes)
		if existing, ok := canonical[key]; ok {
			aliases[step.ID] = existing
			continue
		}
		canonical[key] = step.ID
		out = append(out, step)
	}
	for i := range out {
		for j, dep := range out[i].DependsOn {
			if replacement, ok := aliases[dep]; ok {
				out[i].DependsOn[j] = replacement
			}
		}
	}
	result := Plan{ID: normalized.ID, Version: normalized.Version, Goal: normalized.Goal, Steps: out}
	if err := result.Validate(); err != nil {
		return Plan{}, fmt.Errorf("plan optimizer: %w", err)
	}
	return result.Normalize()
}
