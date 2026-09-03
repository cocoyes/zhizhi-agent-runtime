package exec

import (
	"fmt"
	"sort"

	"github.com/cocoyes/zhizhi-agent-runtime/plan"
)

type Step struct {
	ID          string
	Kind        plan.StepKind
	Importance  plan.StepImportance
	Title       string
	Description string
	Capability  string
	DependsOn   []string
	Optional    bool
	Input       map[string]any
	Bindings    []plan.InputBinding
	Condition   *plan.Condition
}
type Batch struct {
	Index int
	Steps []Step
}
type ExecutionPlan struct {
	Steps        []Step
	Batches      []Batch
	CriticalPath []string
}

// Compile creates dependency-safe parallel batches. Steps in one batch have no
// dependency on another step in the same batch, so a scheduler can run them concurrently.
func Compile(p plan.Plan) (ExecutionPlan, error) {
	normalized, err := p.Normalize()
	if err != nil {
		return ExecutionPlan{}, err
	}
	steps := make(map[string]Step, len(normalized.Steps))
	indegree := make(map[string]int, len(normalized.Steps))
	dependents := make(map[string][]string, len(normalized.Steps))
	for _, item := range normalized.Steps {
		importance := item.Importance
		if importance == "" && item.Optional {
			importance = plan.ImportanceOptional
		}
		if importance == "" {
			importance = plan.ImportanceRequired
		}
		steps[item.ID] = Step{ID: item.ID, Kind: item.Kind, Importance: importance, Title: item.Title, Description: item.Description, Capability: item.Capability, DependsOn: append([]string(nil), item.DependsOn...), Optional: item.Optional || importance == plan.ImportanceOptional, Input: item.Input, Bindings: append([]plan.InputBinding(nil), item.Bindings...), Condition: item.Condition}
		indegree[item.ID] = len(item.DependsOn)
		for _, dep := range item.DependsOn {
			dependents[dep] = append(dependents[dep], item.ID)
		}
	}
	ready := make([]string, 0)
	for id, n := range indegree {
		if n == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	result := ExecutionPlan{Steps: make([]Step, 0, len(steps))}
	seen := 0
	batchIndex := 1
	for len(ready) > 0 {
		current := append([]string(nil), ready...)
		ready = ready[:0]
		batch := Batch{Index: batchIndex, Steps: make([]Step, 0, len(current))}
		for _, id := range current {
			batch.Steps = append(batch.Steps, steps[id])
			result.Steps = append(result.Steps, steps[id])
			seen++
			for _, child := range dependents[id] {
				indegree[child]--
				if indegree[child] == 0 {
					ready = append(ready, child)
				}
			}
		}
		sort.Slice(batch.Steps, func(i, j int) bool { return batch.Steps[i].ID < batch.Steps[j].ID })
		sort.Strings(ready)
		result.Batches = append(result.Batches, batch)
		batchIndex++
	}
	if seen != len(steps) {
		return ExecutionPlan{}, fmt.Errorf("compile: dependency cycle")
	}
	result.CriticalPath = criticalPath(result.Batches)
	return result, nil
}

func criticalPath(batches []Batch) []string {
	out := make([]string, 0, len(batches))
	for _, batch := range batches {
		if len(batch.Steps) > 0 {
			out = append(out, batch.Steps[0].ID)
		}
	}
	return out
}
