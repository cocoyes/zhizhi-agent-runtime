package policy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
)

type ErrorKind string

const (
	Transient ErrorKind = "transient"
	Permanent ErrorKind = "permanent"
	Ambiguous ErrorKind = "ambiguous"
)

type Classification struct {
	Kind      ErrorKind
	Retryable bool
}

func Classify(err error) Classification {
	if err == nil {
		return Classification{}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Classification{Kind: Transient, Retryable: true}
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"timeout", "temporar", "unavailable", "rate limit", "429", "connection reset", "network"} {
		if strings.Contains(text, marker) {
			return Classification{Kind: Transient, Retryable: true}
		}
	}
	for _, marker := range []string{"ambiguous", "unknown effect", "connection lost after"} {
		if strings.Contains(text, marker) {
			return Classification{Kind: Ambiguous}
		}
	}
	return Classification{Kind: Permanent}
}

type Config struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// RetryOn limits automatic retries to the selected failure classes. An
	// empty list preserves the default transient-only behavior.
	RetryOn []ErrorKind
}
type Runner struct {
	Config     Config
	BeforeCall func(tool.Tool) error
	OnEvent    func(Event)
}
type Event struct {
	Type      string
	ToolID    string
	Attempt   int
	Fallback  int
	ErrorKind ErrorKind
	Err       error
}
type Result struct {
	ToolID    string
	Value     tool.Result
	Attempts  int
	Fallbacks int
}

func (r Runner) Execute(ctx context.Context, candidates []tool.Tool, payload []byte) (Result, error) {
	if len(candidates) == 0 {
		return Result{}, fmt.Errorf("policy: no tool candidates")
	}
	maxAttempts := r.Config.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 2
	}
	backoff := r.Config.InitialBackoff
	if backoff <= 0 {
		backoff = 20 * time.Millisecond
	}
	lastErr := error(nil)
	for candidateIndex, candidate := range candidates {
		if candidateIndex > 0 && r.OnEvent != nil {
			r.OnEvent(Event{Type: "tool.fallback", ToolID: candidate.Spec().ID, Fallback: candidateIndex})
		}
		attempts := 0
		for attempts < maxAttempts {
			attempts++
			if r.BeforeCall != nil {
				if err := r.BeforeCall(candidate); err != nil {
					lastErr = err
					break
				}
			}
			value, err := candidate.Call(ctx, payload)
			if err == nil {
				return Result{ToolID: candidate.Spec().ID, Value: value, Attempts: attempts, Fallbacks: candidateIndex}, nil
			}
			lastErr = err
			decision := Classify(err)
			if !retryClassAllowed(decision.Kind, r.Config.RetryOn) {
				break
			}
			// Never repeat an unknown or non-idempotent side effect.
			if !decision.Retryable || !candidate.Spec().Retryable || candidate.Spec().Idempotency != tool.IdempotencySafe || candidate.Spec().IsWrite() {
				break
			}
			if attempts >= maxAttempts {
				break
			}
			if r.OnEvent != nil {
				r.OnEvent(Event{Type: "tool.retry", ToolID: candidate.Spec().ID, Attempt: attempts, ErrorKind: decision.Kind, Err: err})
			}
			wait := backoff
			if r.Config.MaxBackoff > 0 && wait > r.Config.MaxBackoff {
				wait = r.Config.MaxBackoff
			}
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return Result{ToolID: candidate.Spec().ID, Attempts: attempts}, ctx.Err()
			}
			if backoff < time.Second {
				backoff *= 2
			}
		}
	}
	return Result{}, lastErr
}

func retryClassAllowed(kind ErrorKind, allowed []ErrorKind) bool {
	if len(allowed) == 0 {
		return kind == Transient
	}
	for _, value := range allowed {
		if value == kind {
			return true
		}
	}
	return false
}
