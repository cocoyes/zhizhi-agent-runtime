package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/policy"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type StepResult struct {
	StepID              string
	RequestedCapability string
	Capability          string
	ToolID              string
	Content             any
	Receipt             bool
	Action              *tool.ActionReceipt
	Err                 error
	Duration            time.Duration
	Attempts            int
	Fallbacks           int
	Skipped             bool
	ConditionUnknown    bool
	Input               json.RawMessage
	ModelCalls          int
	PromptTokens        int
	CompletionTokens    int
	TotalTokens         int
}

type InputResolver func(Step) json.RawMessage
type AgenticResult struct {
	Content          any
	Action           *tool.ActionReceipt
	ToolCalls        int
	ModelCalls       int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	PendingArguments json.RawMessage
}

type Approval struct {
	ToolID    string
	Arguments json.RawMessage
}
type AgenticExecutor func(context.Context, Step, json.RawMessage) (AgenticResult, error)

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
	// Completed replays successful checkpointed steps without executing them.
	Completed map[string]StepResult
	// Approvals bind resume authorization to an exact step, tool and arguments.
	Approvals map[string]Approval
	Agentic   AgenticExecutor
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
	for id, result := range s.Completed {
		if result.Err == nil && !result.Skipped {
			evidence[id] = result.Content
		}
	}
	for _, batch := range execution.Batches {
		batchResults := make([]StepResult, len(batch.Steps))
		var wg sync.WaitGroup
		sem := make(chan struct{}, s.MaxParallel)
		for i, step := range batch.Steps {
			wg.Add(1)
			go func(index int, current Step) {
				defer wg.Done()
				if completed, ok := s.Completed[current.ID]; ok {
					batchResults[index] = completed
					return
				}
				started := time.Now()
				result := StepResult{StepID: current.ID, RequestedCapability: current.Capability, Capability: current.Capability}
				if current.Condition != nil {
					matched, ok := conditionMatches(evidence[current.Condition.SourceStep], current.Condition)
					if !ok || !matched {
						result.Skipped = true
						result.ConditionUnknown = !ok
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
				if current.Mode == plan.StepModeAgentic {
					if s.Agentic == nil {
						result.Err = errors.New("scheduler: agentic executor is required")
						result.Duration = time.Since(started)
						batchResults[index] = result
						return
					}
					payload, resolveErr := resolvePayload(current, resolve, evidence)
					if resolveErr != nil {
						result.Err = resolveErr
						result.Duration = time.Since(started)
						batchResults[index] = result
						return
					}
					result.Input = append(json.RawMessage(nil), payload...)
					if approval, ok := s.Approvals[current.ID]; ok {
						current.ApprovedToolID = approval.ToolID
						current.ApprovedInput = append([]byte(nil), approval.Arguments...)
					}
					out, runErr := s.Agentic(ctx, current, payload)
					result.Content, result.Action, result.Err = out.Content, out.Action, runErr
					if result.Action != nil {
						result.ToolID = result.Action.ToolID
						result.Action.StepID = current.ID
					}
					if len(out.PendingArguments) > 0 {
						result.Input = append(json.RawMessage(nil), out.PendingArguments...)
					}
					result.Attempts = out.ToolCalls
					result.ModelCalls = out.ModelCalls
					result.PromptTokens = out.PromptTokens
					result.CompletionTokens = out.CompletionTokens
					result.TotalTokens = out.TotalTokens
					result.Duration = time.Since(started)
					batchResults[index] = result
					return
				}
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
				payload, resolveErr := resolvePayload(current, resolve, evidence)
				if resolveErr != nil {
					result.Err = resolveErr
					result.Duration = time.Since(started)
					batchResults[index] = result
					return
				}
				result.Input = append(json.RawMessage(nil), payload...)
				candidateSpec := candidates[0].Spec()
				approval, approvedStep := s.Approvals[current.ID]
				approvedStep = approvedStep && approval.ToolID == candidateSpec.ID && jsonEqual(approval.Arguments, payload)
				if candidateSpec.RequiresConfirmation() && !approvedStep {
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
				if err != nil && out.ToolID != "" {
					if invoked, ok := s.Registry.Get(out.ToolID); ok && invoked.Spec().IsWrite() && out.ErrorKind == policy.Ambiguous {
						now := time.Now().UTC()
						result.Action = &tool.ActionReceipt{ToolID: out.ToolID, Status: contract.ActionUnknown, ExecutedAt: now, Timestamp: now, Message: "write outcome is ambiguous; automatic retry and fallback were blocked"}
					}
				}
				if result.Action != nil {
					result.Action.StepID = current.ID
				}
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
		}
		// Every goroutine in the batch has already completed. Preserve all of
		// their results before reporting a required failure so successful peers
		// can be reused after replanning.
		for _, result := range batchResults {
			if result.Err != nil && !isOptional(result.StepID, batch.Steps) {
				return results, fmt.Errorf("scheduler: step %s: %w", result.StepID, result.Err)
			}
		}
		for _, result := range batchResults {
			if result.Action != nil && result.Action.Status == contract.ActionConfirmationRequired {
				return results, nil
			}
		}
		if err := ctx.Err(); err != nil {
			return results, err
		}
	}
	return results, nil
}

func jsonEqual(left, right json.RawMessage) bool {
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return string(left) == string(right)
	}
	return reflect.DeepEqual(a, b)
}

func conditionMatches(source any, condition *plan.Condition) (bool, bool) {
	encoded, err := json.Marshal(source)
	if err != nil {
		return false, false
	}
	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return false, false
	}
	value, ok := Lookup(generic, condition.SourcePath)
	if !ok {
		return false, false
	}
	if condition.NotEquals != nil {
		return !reflect.DeepEqual(value, normalizeConditionValue(condition.NotEquals)), true
	}
	return reflect.DeepEqual(value, normalizeConditionValue(condition.Equals)), true
}

func resolvePayload(current Step, resolve InputResolver, evidence map[string]any) (json.RawMessage, error) {
	payload := json.RawMessage(`{}`)
	if resolve != nil {
		return resolve(current), nil
	}
	if current.Input == nil && len(current.Bindings) == 0 {
		return payload, nil
	}
	input := current.Input
	if input == nil {
		input = map[string]any{}
	}
	bound, err := ApplyBindings(input, evidence, current.Bindings)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(bound)
	if err != nil {
		return nil, fmt.Errorf("scheduler: encode step %s input: %w", current.ID, err)
	}
	return encoded, nil
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
