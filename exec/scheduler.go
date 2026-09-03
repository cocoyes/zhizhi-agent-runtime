package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/policy"
	bindingresolve "github.com/cocoyes/zhizhi-agent-runtime/resolve"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type StepResult struct {
	StepID     string
	Capability string
	ToolID     string
	Content    any
	Receipt    bool
	Action     *tool.ActionReceipt
	Err        error
	Duration   time.Duration
	Attempts   int
	Fallbacks  int
	Skipped    bool
	Input      json.RawMessage
}

type InputResolver func(Step) json.RawMessage

type Scheduler struct {
	Registry    *tool.Registry
	MaxParallel int
	Policy      policy.Runner
	Guard       func(context.Context, tool.Tool) error
	// Confirm is called before a tool that declares a confirmation policy is
	// executed. Returning false produces a confirmation-required receipt and
	// leaves the external system untouched.
	Confirm  func(context.Context, tool.Spec, json.RawMessage) (bool, error)
	Deadline DeadlinePolicy
	Barrier  *Barrier
	Observe  func(policy.Event)
}

type DeadlinePolicy struct {
	TargetLatency  time.Duration
	HardTimeout    time.Duration
	OptionalCutoff time.Duration
}
type Barrier struct {
	mu          sync.Mutex
	activeWrite bool
}

func (b *Barrier) acquire(ctx context.Context, spec tool.Spec) error {
	if b == nil || !spec.IsWrite() {
		return nil
	}
	for {
		b.mu.Lock()
		if !b.activeWrite {
			b.activeWrite = true
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}
func (b *Barrier) release(spec tool.Spec) {
	if b != nil && spec.IsWrite() {
		b.mu.Lock()
		b.activeWrite = false
		b.mu.Unlock()
	}
}

func (s Scheduler) Run(ctx context.Context, execution ExecutionPlan, resolve InputResolver) ([]StepResult, error) {
	if s.Deadline.HardTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Deadline.HardTimeout)
		defer cancel()
	}
	if s.Registry == nil {
		return nil, fmt.Errorf("scheduler: registry is required")
	}
	if s.MaxParallel < 1 {
		s.MaxParallel = 4
	}
	results := make([]StepResult, 0, len(execution.Steps))
	evidence := make(map[string]any, len(execution.Steps))
	for _, batch := range execution.Batches {
		batchResults := make([]StepResult, len(batch.Steps))
		var wg sync.WaitGroup
		sem := make(chan struct{}, s.MaxParallel)
		for i, step := range batch.Steps {
			wg.Add(1)
			go func(index int, current Step) {
				defer wg.Done()
				started := time.Now()
				result := StepResult{StepID: current.ID, Capability: current.Capability}
				if current.Condition != nil {
					matched, ok := conditionMatches(evidence[current.Condition.SourceStep], current.Condition.SourcePath, current.Condition.Equals)
					if !ok || !matched {
						result.Skipped = true
						result.Duration = time.Since(started)
						batchResults[index] = result
						return
					}
				}
				if current.Optional && s.Deadline.OptionalCutoff > 0 && time.Until(deadlineFromContext(ctx)) <= s.Deadline.OptionalCutoff {
					result.Skipped = true
					result.Err = fmt.Errorf("deadline: optional step skipped")
					result.Duration = time.Since(started)
					batchResults[index] = result
					return
				}
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					result.Err = ctx.Err()
					result.Duration = time.Since(started)
					batchResults[index] = result
					return
				}
				defer func() { <-sem }()
				candidates := s.Registry.ResolveCapability(current.Capability)
				if len(candidates) == 0 {
					result.Err = fmt.Errorf("capability %q has no registered tool", current.Capability)
					result.Duration = time.Since(started)
					batchResults[index] = result
					return
				}
				if err := s.Barrier.acquire(ctx, candidates[0].Spec()); err != nil {
					result.Err = err
					result.Duration = time.Since(started)
					batchResults[index] = result
					return
				}
				defer s.Barrier.release(candidates[0].Spec())
				payload := json.RawMessage(`{}`)
				if resolve != nil {
					payload = resolve(current)
				} else if current.Input != nil || len(current.Bindings) > 0 {
					input := current.Input
					if input == nil {
						input = map[string]any{}
					}
					bound, bindErr := bindingresolve.ApplyBindings(input, evidence, current.Bindings)
					if bindErr != nil {
						result.Err = bindErr
						result.Duration = time.Since(started)
						batchResults[index] = result
						return
					}
					payload, _ = json.Marshal(bound)
				}
				result.Input = append(json.RawMessage(nil), payload...)
				candidateSpec := candidates[0].Spec()
				if candidateSpec.RequiresConfirmation() {
					approved := false
					if s.Confirm != nil {
						var confirmErr error
						approved, confirmErr = s.Confirm(ctx, candidateSpec, payload)
						if confirmErr != nil {
							result.Err = confirmErr
							result.Duration = time.Since(started)
							batchResults[index] = result
							return
						}
					}
					if !approved {
						now := time.Now().UTC()
						result.ToolID = candidateSpec.ID
						result.Action = &tool.ActionReceipt{ToolID: candidateSpec.ID, Status: contract.ActionConfirmationRequired, ExecutedAt: now, Timestamp: now, Message: "explicit confirmation is required before execution"}
						result.Duration = time.Since(started)
						batchResults[index] = result
						return
					}
				}
				runner := s.Policy
				runner.OnEvent = s.Observe
				runner.BeforeCall = func(candidate tool.Tool) error {
					if s.Guard == nil {
						return nil
					}
					return s.Guard(ctx, candidate)
				}
				out, err := runner.Execute(ctx, candidates, payload)
				result.ToolID, result.Content, result.Receipt, result.Action, result.Err = out.ToolID, out.Value.Content, out.Value.Receipt, out.Value.Action, err
				result.Attempts, result.Fallbacks = out.Attempts, out.Fallbacks
				result.Duration = time.Since(started)
				batchResults[index] = result
			}(i, step)
		}
		wg.Wait()
		for _, result := range batchResults {
			results = append(results, result)
			if result.Err == nil {
				evidence[result.StepID] = result.Content
			}
			if result.Err != nil && !isOptional(result.StepID, batch.Steps) {
				return results, fmt.Errorf("scheduler: step %s: %w", result.StepID, result.Err)
			}
		}
		if err := ctx.Err(); err != nil {
			return results, err
		}
	}
	return results, nil
}

func conditionMatches(source any, path string, expected any) (bool, bool) {
	encoded, err := json.Marshal(source)
	if err != nil {
		return false, false
	}
	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return false, false
	}
	value, ok := bindingresolve.Lookup(generic, path)
	if !ok {
		return false, false
	}
	return reflect.DeepEqual(value, normalizeConditionValue(expected)), true
}

func normalizeConditionValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return value
	}
	return normalized
}

func deadlineFromContext(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now().Add(365 * 24 * time.Hour)
}

func isOptional(id string, steps []Step) bool {
	for _, step := range steps {
		if step.ID == id {
			return step.Optional
		}
	}
	return false
}
