package zhizhi

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/middleware"
	"github.com/cocoyes/zhizhi-agent-runtime/skill"
	"github.com/cocoyes/zhizhi-agent-runtime/skill/script"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type scriptToolInput struct {
	SkillID    string   `json:"skill_id"`
	ScriptPath string   `json:"script_path"`
	Args       []string `json:"args,omitempty"`
}
type skillScriptTool struct {
	tool.Tool
	scope  *skillScope
	loaded skill.Loaded
}

func (s *skillScope) executionTools(loaded skill.Loaded) []tool.Tool {
	values := s.businessTools()
	executor := s.runtime.cfg.skillConfig.options.ScriptExecutor
	if executor == nil {
		return values
	}
	prototype := tool.Func("run_skill_script", "Execute an explicitly approved Python entrypoint of the currently selected Skill when its instructions require it. Supply only skill_id, relative script_path and bounded args. This is not a shell. Use /workspace for task intermediates and /output for deliverable files.", func(context.Context, scriptToolInput) (script.Result, error) { return script.Result{}, nil }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyNonIdempotent), tool.WithRetryable(false), tool.WithRisk(tool.RiskMedium), tool.WithConfirmation(s.runtime.cfg.skillConfig.options.ScriptConfirmation), tool.WithVersion(executor.Fingerprint()))
	if s.filter != nil && !s.filter(prototype.Spec()) {
		return values
	}
	bound := &skillScriptTool{Tool: prototype, scope: s, loaded: loaded}
	return append(values, middleware.WrapTool(bound, s.resourceCalls, s.resourceStreams))
}

func (t *skillScriptTool) Call(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if err := tool.ValidateInput(t.Spec(), raw); err != nil {
		return tool.Result{}, err
	}
	var in scriptToolInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Result{}, err
	}
	if in.SkillID != t.loaded.Name || !script.ValidPath(in.ScriptPath) {
		return tool.Result{Content: script.Result{Code: script.CodePermission, ExitCode: -1, Files: []script.File{}}}, nil
	}
	if err := script.ValidateArgs(in.Args); err != nil {
		return tool.Result{Content: script.Result{Code: script.CodeInvalidArgs, ExitCode: -1, Files: []script.File{}}}, nil
	}
	s := t.scope
	executor := s.runtime.cfg.skillConfig.options.ScriptExecutor
	s.mu.Lock()
	if s.scriptWorkspace == nil {
		workspace, err := executor.CreateWorkspace(ctx, s.runID)
		if err != nil {
			s.mu.Unlock()
			return tool.Result{}, err
		}
		s.scriptWorkspace = &workspace
	}
	workspace := *s.scriptWorkspace
	s.mu.Unlock()
	s.emit("skill.script.started", map[string]any{"skill_name": in.SkillID})
	result, err := executor.Execute(ctx, script.Request{SkillID: in.SkillID, SkillFingerprint: t.loaded.Fingerprint, ScriptPath: in.ScriptPath, Args: append([]string(nil), in.Args...), Workspace: workspace})
	output := tool.Result{Content: result}
	if result.Started {
		status := contract.ActionSuccess
		if !result.Success {
			status = contract.ActionFailed
		}
		if err != nil && result.ExitCode < 0 {
			status = contract.ActionUnknown
		}
		now := time.Now().UTC()
		output.Receipt = true
		output.Action = &contract.ActionReceipt{ToolID: t.Spec().ID, Status: status, ExternalID: workspace.ID, ExecutedAt: now, Timestamp: now, Message: string(result.Code)}
	}
	phase := "completed"
	if !result.Success {
		phase = "failed"
	}
	s.emit("skill.script."+phase, map[string]any{"skill_name": in.SkillID, "code": result.Code, "exit_code": result.ExitCode, "duration_ms": result.DurationMS})
	// A known nonzero exit is data the model may explain or repair explicitly;
	// transport failures, limits and cancellation remain non-retryable errors.
	var coded *script.Error
	if errors.As(err, &coded) && (coded.Code == script.CodeScriptFailed || coded.Code == script.CodeInvalidArgs || coded.Code == script.CodePermission || coded.Code == script.CodeScriptMissing) {
		err = nil
	}
	return output, err
}

func markSkillSuspended(ctx context.Context) {
	if s, ok := ctx.Value(skillScopeKey{}).(*skillScope); ok {
		s.mu.Lock()
		s.retainWorkspace = true
		s.mu.Unlock()
	}
}

func (s *skillScope) finish() {
	if s == nil {
		return
	}
	s.finishOnce.Do(func() {
		s.mu.Lock()
		retained, w := s.retainWorkspace, s.scriptWorkspace
		s.mu.Unlock()
		if retained || w == nil {
			return
		}
		executor := s.runtime.cfg.skillConfig.options.ScriptExecutor
		if executor == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := executor.ReleaseWorkspace(ctx, *w); err != nil {
			s.emit("skill.workspace.release_failed", map[string]any{"code": "cleanup_failed"})
		}
	})
}
