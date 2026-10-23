package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/internal/budget"
	"github.com/cocoyes/zhizhi-agent-runtime/internal/structuredoutput"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

// StructuredOutputError identifies a failed candidate without exposing model text.
// Cause remains available through errors.Is/errors.As for diagnostics.
type StructuredOutputError struct {
	Phase          string
	Kind           string
	Path           string
	Attempts       int
	RepairAttempts int
	Recoverable    bool
	Cause          error
}

func (e *StructuredOutputError) Error() string {
	return fmt.Sprintf("structured output failed (phase=%s kind=%s attempts=%d repairs=%d)", e.Phase, e.Kind, e.Attempts, e.RepairAttempts)
}
func (e *StructuredOutputError) Unwrap() error { return e.Cause }

// ModelError preserves a provider failure without placing provider text in logs.
type ModelError struct{ Cause error }

func (e *ModelError) Error() string { return "planning model call failed" }
func (e *ModelError) Unwrap() error { return e.Cause }

func repairLimit(value *int) int {
	if value == nil {
		return 1
	}
	if *value < 0 {
		return 0
	}
	return *value
}

type validationIssue struct {
	kind  string
	path  string
	cause error
}

func (e *validationIssue) Error() string   { return e.cause.Error() }
func (e *validationIssue) Unwrap() error   { return e.cause }
func invalid(kind string, err error) error { return &validationIssue{kind: kind, cause: err} }

func invalidAt(kind, path string, err error) error {
	return &validationIssue{kind: kind, path: path, cause: err}
}

func validateCandidateSchema[T any](text string) error {
	var generic any
	if err := structuredoutput.DecodeStrict(text, &generic); err != nil {
		return invalid("decode", err)
	}
	root, _ := generic.(map[string]any)
	for _, field := range []string{"steps", "add_steps", "replace_steps"} {
		steps, _ := root[field].([]any)
		for index, raw := range steps {
			step, _ := raw.(map[string]any)
			base := fmt.Sprintf("/%s/%d", field, index)
			required := []string{"id", "mode"}
			if step["mode"] == "tool" {
				required = append(required, "capability")
			}
			if step["mode"] == "agentic" {
				required = append(required, "goal", "success_criteria", "capabilities", "tool_budget")
			}
			for _, key := range required {
				if _, exists := step[key]; !exists {
					return invalidAt("schema", base+"/"+key, errors.New("required step field missing"))
				}
			}
			if step["mode"] == "agentic" {
				toolBudget, _ := step["tool_budget"].(map[string]any)
				for _, key := range []string{"min", "max", "required_successes"} {
					if _, exists := toolBudget[key]; !exists {
						return invalidAt("schema", base+"/tool_budget/"+key, errors.New("required budget field missing"))
					}
				}
			}
		}
	}
	t, err := outputTool[T]("validate_output", "")
	if err != nil {
		return err
	}
	if err = tool.ValidateValue("structured_output", t.Function.Parameters, generic); err != nil {
		return invalid("schema", err)
	}
	return nil
}

// outputTool shares a mode-discriminated step schema between initial plans and patches.
func outputTool[T any](name, description string) (model.ToolSpec, error) {
	t, err := structuredoutput.ToolFor[T](name, description)
	if err != nil {
		return t, err
	}
	var root map[string]any
	if err = json.Unmarshal(t.Function.Parameters, &root); err != nil {
		return t, err
	}
	var visit func(map[string]any)
	visit = func(node map[string]any) {
		props, _ := node["properties"].(map[string]any)
		if props["capability"] != nil && props["tool_budget"] != nil {
			variants := []any{}
			for _, mode := range []string{"tool", "agentic"} {
				p := map[string]any{}
				for key, value := range props {
					agentic := key == "capabilities" || key == "tool_budget"
					tool := key == "capability" || key == "bindings"
					if (mode == "tool" && agentic) || (mode == "agentic" && tool) {
						continue
					}
					p[key] = value
				}
				p["mode"] = map[string]any{"type": "string", "const": mode}
				p["id"] = map[string]any{"type": "string", "minLength": 1}
				if mode == "tool" {
					p["capability"] = map[string]any{"type": "string", "minLength": 1}
				} else {
					p["goal"] = map[string]any{"type": "string", "minLength": 1}
					p["success_criteria"] = map[string]any{"type": "string", "minLength": 1}
					p["capabilities"] = map[string]any{"type": "array", "minItems": 1, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1}}
					p["tool_budget"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"min", "max", "required_successes"}, "properties": map[string]any{"min": map[string]any{"type": "integer", "minimum": 0}, "max": map[string]any{"type": "integer", "minimum": 1}, "required_successes": map[string]any{"type": "integer", "minimum": 0}}}
				}
				required := []string{"id", "mode", "capability"}
				if mode == "agentic" {
					required = []string{"id", "mode", "goal", "success_criteria", "capabilities", "tool_budget"}
				}
				variants = append(variants, map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false})
			}
			for k := range node {
				delete(node, k)
			}
			node["oneOf"] = variants
			return
		}
		for _, v := range node {
			switch x := v.(type) {
			case map[string]any:
				visit(x)
			case []any:
				for _, item := range x {
					if m, ok := item.(map[string]any); ok {
						visit(m)
					}
				}
			}
		}
	}
	visit(root)
	t.Function.Parameters, err = json.Marshal(root)
	return t, err
}

// repairClass narrows a rejected candidate to a fixable category. The repair
// turn is the only chance the model gets, so it must say which rule was broken
// rather than repeating a generic "try again".
func repairClass(kind string, err error) string {
	switch kind {
	case "candidate_missing":
		return "missing_output"
	case "decode":
		return "decode"
	case "schema":
		return "schema"
	case "capability_reference":
		return "capability"
	}
	switch ClassifyError(err) {
	case "schema_missing", "schema_null":
		return "schemaless_binding"
	case "budget_range", "budget_invalid":
		return "budget"
	case "circular_dependency":
		return "cycle"
	case "mode_conflict":
		return "mode"
	case "binding_invalid":
		return "binding"
	}
	return "semantic"
}

func repairGuidance(class, path string) string {
	prefix := "The previous output was rejected. Emit the complete result again, corrected."
	if path != "" && path != "/" {
		prefix += " The rejection is at JSON pointer " + path + "."
	}
	switch class {
	case "missing_output":
		return prefix + " You must call the required output tool with the schema arguments; prose or an unrelated tool call is not accepted."
	case "decode":
		return prefix + " The payload was not valid JSON for the schema. Use double-quoted keys and strings, no comments, no trailing commas, no Markdown fences."
	case "schema":
		return prefix + " The payload is valid JSON but does not match the required schema. Every step needs id and mode; a tool step needs capability; an agentic step needs goal, success_criteria, capabilities and tool_budget{min,max,required_successes}. Do not add fields the schema does not define, and do not omit required ones."
	case "capability":
		return prefix + " Every capability and agentic capabilities entry must be copied verbatim from the tool catalog. Do not invent, translate, or abbreviate tool names."
	case "schemaless_binding":
		return prefix + " A binding source tool declares no output schema, so its runtime result cannot be referenced. Delete that binding and merge the steps that depend on that tool into a single mode \"agentic\" step whose capabilities list only the tools needed, with an explicit goal, success_criteria, and a finite tool_budget. Agentic steps must not carry bindings."
	case "budget":
		return prefix + " The agentic tool_budget is impossible. Set 0 <= min <= max, 0 <= required_successes <= max, and max >= 1."
	case "cycle":
		return prefix + " The depends_on graph contains a cycle. Remove the back edge so execution has a topological order."
	case "mode":
		return prefix + " A tool step and an agentic step were mixed: tool steps need capability, depends_on, input, bindings, condition; agentic steps need goal, success_criteria, capabilities, tool_budget and must not carry bindings or a capability field."
	case "binding":
		return prefix + " Bindings must reference an existing dependency as source_step, a source_path that the source tool output_schema actually defines, and a target_path that exists in the current tool input_schema. Every required input must be supplied by input or bindings."
	}
	return prefix + " Fix the specific validation error shown and keep every step that was not reported as invalid unchanged."
}

// generateCandidate centralizes repair accounting and safe lifecycle traces.
func generateCandidate[T any](ctx context.Context, m model.Model, phase string, limit *int, useTool bool, messages []model.Message, t model.ToolSpec, trace func(string, map[string]any), decode func(string) (T, error)) (T, error) {
	var zero T
	if !useTool {
		messages = append([]model.Message(nil), messages...)
		messages[0].Content += "\nReturn JSON matching this exact schema (including explicit step mode):\n" + string(t.Function.Parameters)
	}
	var last error
	// lastValue is the most recent candidate that parsed far enough to be
	// useful. Returning it with the error lets a rule fixer repair a plan the
	// validator rejected instead of a zero value.
	var lastValue T
	var candidate string
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if last == nil {
				return zero, &ModelError{Cause: err}
			}
			repairs := attempt - 1
			if repairs < 0 {
				repairs = 0
			}
			return lastValue, &StructuredOutputError{Phase: phase, Kind: "semantic", Path: "/", Attempts: attempt, RepairAttempts: repairs, Recoverable: false, Cause: errors.Join(err, last)}
		}
		request := structuredModelInput(messages, t, useTool)
		request.JSONMode = !useTool && m.Capabilities().JSONMode
		if attempt > 0 {
			trace("repair.started", map[string]any{"attempt": attempt, "repair_attempt": attempt, "kind": "semantic", "path": "/", "model_id": m.ID()})
		}
		started := time.Now()
		out, err := m.Generate(ctx, request)
		if out == nil && err == nil {
			err = errors.New("model returned nil output")
		}
		if err != nil {
			err = &ModelError{Cause: err}
			if attempt > 0 {
				trace("repair.completed", map[string]any{"attempt": attempt, "repair_attempt": attempt, "latency_ms": time.Since(started).Milliseconds(), "validation_succeeded": false})
			}
			if last != nil {
				attempts, repairs := attempt+1, attempt
				if errors.Is(err, budget.Exceeded) {
					attempts--
					repairs--
					if repairs < 0 {
						repairs = 0
					}
				}
				return lastValue, &StructuredOutputError{Phase: phase, Kind: "semantic", Path: "/", Attempts: attempts, RepairAttempts: repairs, Recoverable: false, Cause: errors.Join(err, last)}
			}
			return zero, err
		}
		event := "model.completed"
		if attempt > 0 {
			event = "repair.output"
		}
		trace(event, map[string]any{"content": out.Text, "reasoning_content": out.ReasoningContent, "tool_call_requests": out.ToolCalls})
		candidate, err = structuredoutput.Candidate(out, t.Function.Name, useTool)
		kind := "candidate_missing"
		path := "/"
		var result T
		if err == nil {
			kind = "semantic"
			result, err = decode(candidate)
			var issue *validationIssue
			if errors.As(err, &issue) {
				kind = issue.kind
				if issue.path != "" {
					path = issue.path
				}
			}
			lastValue = result
		}
		if attempt > 0 {
			trace("repair.completed", map[string]any{"attempt": attempt, "repair_attempt": attempt, "kind": kind, "path": "/", "model_id": m.ID(), "latency_ms": time.Since(started).Milliseconds(), "validation_succeeded": err == nil})
		}
		if err == nil {
			return result, nil
		}
		last = err
		trace("candidate.invalid", map[string]any{"attempt": attempt + 1, "kind": kind, "path": path, "model_id": m.ID(), "latency_ms": time.Since(started).Milliseconds()})
		if attempt >= repairLimit(limit) {
			return lastValue, &StructuredOutputError{Phase: phase, Kind: kind, Path: path, Attempts: attempt + 1, RepairAttempts: attempt, Recoverable: true, Cause: err}
		}
		class := repairClass(kind, err)
		trace("repair.guidance", map[string]any{"attempt": attempt, "kind": kind, "class": class, "path": path})
		repairRequest := messages[len(messages)-1].Content + "\nInvalid candidate:\n" + candidate + "\nValidation error:\n" + err.Error()
		trace("repair.request", map[string]any{"content": repairRequest})
		messages = []model.Message{{Role: model.RoleSystem, Content: messages[0].Content + "\n" + repairGuidance(class, path)}, {Role: model.RoleUser, Content: repairRequest}}
	}
}
