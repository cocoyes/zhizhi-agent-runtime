// Package script defines approved Skill entrypoints and task-scoped process
// execution. It is independent of the Agent scheduler and sandbox implementation.
package script

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/skill"
)

// Code is a stable, model-safe execution outcome.
type Code string

const (
	CodeOK                Code = "ok"
	CodeDisabled          Code = "disabled"
	CodeUnsupportedHost   Code = "unsupported_host"
	CodePermission        Code = "permission_denied"
	CodeScriptMissing     Code = "script_not_found"
	CodeDrift             Code = "approval_drift"
	CodeInvalidArgs       Code = "invalid_arguments"
	CodeDockerUnavailable Code = "docker_unavailable"
	CodeImageMissing      Code = "image_missing"
	CodeImagePull         Code = "image_pull_failed"
	CodeContainer         Code = "container_error"
	CodeScriptFailed      Code = "script_failed"
	CodeTimeout           Code = "timeout"
	CodeCanceled          Code = "canceled"
	CodeResourceLimit     Code = "resource_limit"
	CodeLogLimit          Code = "log_limit"
	CodeOutputLimit       Code = "output_limit"
	CodeWorkspaceLimit    Code = "workspace_limit"
	CodeCleanup           Code = "cleanup_failed"
)

var (
	ErrPermission = errors.New("script: permission denied")
	ErrDrift      = errors.New("script: approval drift")
	ErrLimit      = errors.New("script: resource limit")
	ErrClosed     = errors.New("script: executor closed")
)

// Error hides backend paths, credentials and daemon diagnostics from tool output.
// Unwrap retains machine-readable cancellation and permission causes.
type Error struct {
	Code  Code
	cause error
}

func (e *Error) Error() string { return "script execution: " + string(e.Code) }
func (e *Error) Unwrap() error { return e.cause }

// Failure constructs a sanitized error while preserving its cause for errors.Is.
func Failure(code Code, cause error) error { return &Error{Code: code, cause: cause} }

// Workspace is an opaque framework-created reference. It is never a host path.
type Workspace struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
}

// Request is supplied by trusted host code. Models receive only SkillID,
// ScriptPath and Args through the bound tool, never Workspace or Timeout.
type Request struct {
	SkillID          string
	SkillFingerprint string
	ScriptPath       string
	Args             []string
	Workspace        Workspace
	Timeout          time.Duration
}

// File is a safe logical reference, relative to the task workspace.
type File struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Result includes partial logs and known effects on every execution outcome.
type Result struct {
	Success     bool   `json:"success"`
	Code        Code   `json:"code"`
	ExitCode    int    `json:"exit_code"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	DurationMS  int64  `json:"duration_ms"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Files       []File `json:"files"`
	Started     bool   `json:"-"`
}

// Executor is the sandbox boundary. Implementations own task isolation, file
// access and process cleanup; an Agent never assumes a particular runtime.
type Executor interface {
	Fingerprint() string
	CreateWorkspace(context.Context, string) (Workspace, error)
	ValidateWorkspace(context.Context, Workspace) error
	Execute(context.Context, Request) (Result, error)
	ReleaseWorkspace(context.Context, Workspace) error
	OpenFile(context.Context, Workspace, string) (io.ReadCloser, error)
	Close(context.Context) error
}

// EntryPoint explicitly permits one script. Version must change when a custom
// business argument validator changes, so suspended approvals detect policy drift.
type EntryPoint struct {
	Path         string
	Version      string
	ValidateArgs func([]string) error `json:"-"`
}

// Registration is application-owned trust, never metadata read from SKILL.md.
// Files lists extra imports/assets required by Scripts; all files are pinned.
type Registration struct {
	SkillID     string
	Fingerprint string
	Scripts     []EntryPoint
	Files       []string
	Image       string
	Env         map[string]string
}

// Approval pins explicitly registered files at the time of review/initialization.
type Approval struct {
	Metadata skill.Metadata
	Scripts  map[string]EntryPoint
	Files    map[string]string
	Image    string
	Env      map[string]string
}

// ValidPath accepts canonical, relative slash-separated paths only.
func ValidPath(p string) bool {
	if p == "" || len(p) > 512 || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." || strings.Contains(part, ":") {
			return false
		}
	}
	return true
}

// ValidateArgs applies baseline bounds before an optional business validator.
func ValidateArgs(args []string) error {
	if len(args) > 32 {
		return Failure(CodeInvalidArgs, ErrPermission)
	}
	total := 0
	for _, arg := range args {
		total += len(arg)
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return Failure(CodeInvalidArgs, ErrPermission)
		}
	}
	if total > 16<<10 {
		return Failure(CodeInvalidArgs, ErrPermission)
	}
	return nil
}

// Hash returns the SHA-256 of exact approved bytes.
func Hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

// Approve pins only explicitly listed files of a loaded Skill. It does not
// authorize new files after reload, install dependencies or execute anything.
func Approve(ctx context.Context, catalog *skill.Catalog, r Registration) (Approval, error) {
	if catalog == nil || len(r.Scripts) == 0 {
		return Approval{}, Failure(CodePermission, ErrPermission)
	}
	var meta skill.Metadata
	for _, candidate := range catalog.Metadata() {
		if candidate.Name == r.SkillID {
			meta = candidate
			break
		}
	}
	if meta.Name == "" {
		return Approval{}, Failure(CodePermission, ErrPermission)
	}
	if r.Fingerprint != "" && r.Fingerprint != meta.Fingerprint {
		return Approval{}, Failure(CodeDrift, ErrDrift)
	}
	loaded, err := catalog.Load(ctx, meta.Name, meta.Fingerprint)
	if err != nil {
		return Approval{}, Failure(CodeDrift, err)
	}
	out := Approval{Metadata: meta, Scripts: map[string]EntryPoint{}, Files: map[string]string{}, Image: r.Image, Env: map[string]string{}}
	for key, value := range r.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return Approval{}, Failure(CodePermission, ErrPermission)
		}
		out.Env[key] = value
	}
	paths := append([]string(nil), r.Files...)
	for _, entry := range r.Scripts {
		if !ValidPath(entry.Path) || (entry.ValidateArgs != nil && entry.Version == "") {
			return Approval{}, Failure(CodePermission, ErrPermission)
		}
		if _, ok := out.Scripts[entry.Path]; ok {
			return Approval{}, Failure(CodePermission, ErrPermission)
		}
		out.Scripts[entry.Path] = entry
		paths = append(paths, entry.Path)
	}
	if len(paths) > 256 {
		return Approval{}, Failure(CodePermission, ErrLimit)
	}
	total := 0
	for _, p := range paths {
		if !ValidPath(p) {
			return Approval{}, Failure(CodePermission, ErrPermission)
		}
		bytes, hash, err := loaded.ReadFile(ctx, p, 32<<20)
		if err != nil {
			return Approval{}, Failure(CodePermission, ErrPermission)
		}
		total += len(bytes)
		if total > 32<<20 {
			return Approval{}, Failure(CodePermission, ErrLimit)
		}
		out.Files[p] = hash
	}
	return out, nil
}
