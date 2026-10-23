package plan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

func TestStrictPatchAndMetadataContract(t *testing.T) {
	for _, raw := range []string{`{"remove_steps":[{"id":"a"}]}`, `{"remove_steps":[123]}`, `{"bogus":true,"remove_steps":["a"]}`} {
		if _, err := decodePatch(raw); err == nil {
			t.Fatalf("accepted malformed patch %s", raw)
		}
	}
	source := Plan{ID: "stable", Goal: "goal", Version: 3, Steps: []Step{{ID: "a", Capability: "tool"}}}
	out, err := (Patch{Replace: []Step{{ID: "a", Capability: "tool"}}}).Apply(source)
	if err != nil || out.ID != source.ID || out.Goal != source.Goal || out.Version != 4 {
		t.Fatalf("metadata lost: %+v %v", out, err)
	}
	if _, err := (Patch{Remove: []string{"a"}, Add: []Step{{ID: "a", Capability: "tool"}}}).Apply(source); err == nil {
		t.Fatal("accepted conflicting patch IDs")
	}
}

func TestAgenticStepContract(t *testing.T) {
	good := Step{ID: "a", Mode: StepModeAgentic, Goal: "goal", SuccessCriteria: "done", Capabilities: []string{"tool"}, ToolBudget: ToolCallBudget{Max: 2}}
	cases := []Step{good, good, good, good, good}
	cases[0].SuccessCriteria = " "
	cases[1].ToolBudget.Max = 0
	cases[2].ToolBudget.RequiredSuccesses = 3
	cases[3].Capability = "tool"
	cases[4].Capabilities = []string{" "}
	for i, s := range cases {
		if err := (Plan{Steps: []Step{s}}).Validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

type contractModel struct {
	calls       int
	repairError error
	input       model.ModelInput
}

func (m *contractModel) ID() string { return "contract" }
func (m *contractModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{Declared: true}
}
func (m *contractModel) Generate(_ context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	m.input = input
	if m.calls > 1 && m.repairError != nil {
		return nil, m.repairError
	}
	return &model.ModelOutput{Text: `{"version":1,"steps":[{"id":"sensitive-id","mode":"agentic","success_criteria":"done","capabilities":["x"],"tool_budget":{"min":0,"max":1,"required_successes":0}}]}`}, nil
}
func (m *contractModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestStructuredFailureRepairsAndPreservesModelCause(t *testing.T) {
	for _, count := range []int{0, 1} {
		m := &contractModel{}
		p := NewModelPlanner(m)
		p.MaxRepairAttempts = &count
		_, err := p.Plan(context.Background(), "sensitive-user", nil)
		var structured *StructuredOutputError
		if !errors.As(err, &structured) || !structured.Recoverable || structured.Attempts != count+1 || structured.RepairAttempts != count || structured.Phase != "initial_plan" || structured.Path != "/steps/0/goal" {
			t.Fatalf("invalid accounting: %+v %v", structured, err)
		}
		if strings.Contains(err.Error(), "sensitive") || m.input.JSONMode || len(m.input.Tools) > 0 {
			t.Fatalf("unsafe output or unsupported provider request: %v %+v", err, m.input)
		}
	}
	providerErr := errors.New("sensitive-provider-error")
	m := &contractModel{repairError: providerErr}
	_, err := NewModelPlanner(m).Plan(context.Background(), "goal", nil)
	var structured *StructuredOutputError
	if !errors.As(err, &structured) || structured.Recoverable || !errors.Is(err, providerErr) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("model cause or recovery classification lost: %v", err)
	}
}

// The single repair attempt must carry guidance for the specific rule that was
// broken; a generic "try again" wastes it.
func TestRepairTurnCarriesClassSpecificGuidance(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		err      error
		contains string
	}{
		{"schemaless", "semantic", errors.New(`plan: step "b" binding source path "/x" is unavailable because source tool "search" declares no output schema`), "mode \"agentic\""},
		{"budget", "semantic", errors.New("plan: invalid finite agentic tool budget"), "required_successes"},
		{"schema", "schema", errors.New("required step field missing"), "tool_budget{min,max,required_successes}"},
		{"decode", "decode", errors.New("repair JSON: unexpected"), "no Markdown fences"},
		{"capability", "capability_reference", errors.New("plan: capability has no tool"), "copied verbatim from the tool catalog"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class := repairClass(tc.kind, tc.err)
			guidance := repairGuidance(class, "/steps/0/goal")
			if !strings.Contains(guidance, tc.contains) {
				t.Fatalf("guidance for %s did not explain the rule: %s", class, guidance)
			}
			if !strings.Contains(guidance, "/steps/0/goal") {
				t.Fatalf("guidance dropped the failing path: %s", guidance)
			}
		})
	}
	if class := repairClass("semantic", errors.New("plan: dependency cycle at \"a\"")); class != "cycle" {
		t.Fatalf("cycle misclassified as %q", class)
	}
}

func TestSharedAgenticOutputContract(t *testing.T) {
	step := `{"id":"a","mode":"agentic","goal":"observe","success_criteria":"done","capabilities":["x"],"input":{"context":"hint"},"tool_budget":{"min":0,"max":2,"required_successes":0}}`
	value, err := decodePlan(`{"version":1,"steps":[`+step+`]}`, nil)
	if err != nil || len(value.Steps[0].Capabilities) != 1 {
		t.Fatalf("initial agentic: %+v %v", value, err)
	}
	for _, field := range []string{"add_steps", "replace_steps"} {
		if _, err := decodePatch(`{"` + field + `":[` + step + `]}`); err != nil {
			t.Fatalf("%s: %v", field, err)
		}
	}
	if err := ValidatePatchCompatibility(value, Patch{Replace: []Step{{ID: "a", Capability: "x"}}}, nil); err == nil {
		t.Fatal("accepted mode switch")
	}
	if err := ValidatePatchCompatibility(value, Patch{Replace: []Step{{ID: "a", Mode: StepModeAgentic, Capabilities: []string{"y"}}}}, nil); err == nil {
		t.Fatal("accepted widened allowlist")
	}
}

type contractToolModel struct{ contractModel }

func (m *contractToolModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{Declared: true, ToolCalling: true}
}

func TestOutputToolOptOutAndStrictWire(t *testing.T) {
	zero := 0
	m := &contractToolModel{}
	p := NewModelPlanner(m)
	p.DisableOutputTool = true
	p.MaxRepairAttempts = &zero
	_, err := p.Plan(context.Background(), "goal", nil)
	var structured *StructuredOutputError
	if !errors.As(err, &structured) || structured.Kind != "schema" || len(m.input.Tools) > 0 || m.input.JSONMode || !strings.Contains(m.input.Messages[0].Content, "oneOf") {
		t.Fatalf("text compatibility path failed: input=%+v err=%v", m.input, err)
	}
	if _, err := decodePlan(`{"version":1,"steps":[{"id":"a","capability":"x"}]}`, nil); err == nil {
		t.Fatal("model wire accepted missing mode")
	}
	if _, err := (Plan{Steps: []Step{{ID: "a", Capability: "x"}}}).Normalize(); err != nil {
		t.Fatalf("legacy custom plan rejected: %v", err)
	}
	if _, err := decodePatch(`{"add_steps":[{"id":"a","mode":"agentic","goal":"goal","success_criteria":"done","capabilities":["x"],"tool_budget":{"max":2}}]}`); err == nil {
		t.Fatal("model wire accepted missing budget fields")
	}
}
