package policy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/tool"
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
	ToolID      string
	Value       tool.Result
	Attempts    int
	Fallbacks   int
	AttemptsLog []AttemptRecord
	ErrorKind   ErrorKind
}

type AttemptRecord struct {
	ToolID   string
	Attempt  int
	Started  time.Time
	Duration time.Duration
	Error    string
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
	lastToolID := ""
	totalAttempts := 0
	lastFallback := 0
	lastKind := ErrorKind("")
	records := make([]AttemptRecord, 0)
	for candidateIndex, candidate := range candidates {
		if candidateIndex > 0 && r.OnEvent != nil {
			r.OnEvent(Event{Type: "tool.fallback", ToolID: candidate.Spec().ID, Fallback: candidateIndex})
		}
		attempts := 0
		for attempts < maxAttempts {
			attempts++
			totalAttempts++
			lastToolID = candidate.Spec().ID
			lastFallback = candidateIndex
			started := time.Now()
			if r.BeforeCall != nil {
				if err := r.BeforeCall(candidate); err != nil {
					lastErr = err
					records = append(records, AttemptRecord{ToolID: lastToolID, Attempt: attempts, Started: started, Duration: time.Since(started), Error: err.Error()})
					break
				}
			}
			value, err := candidate.Call(ctx, payload)
			if err == nil {
				records = append(records, AttemptRecord{ToolID: lastToolID, Attempt: attempts, Started: started, Duration: time.Since(started)})
				return Result{ToolID: lastToolID, Value: value, Attempts: totalAttempts, Fallbacks: candidateIndex, AttemptsLog: records}, nil
			}
			lastErr = err
			records = append(records, AttemptRecord{ToolID: lastToolID, Attempt: attempts, Started: started, Duration: time.Since(started), Error: err.Error()})
			decision := Classify(err)
			lastKind = decision.Kind
			// A transport-style failure after invoking a write cannot establish
			// whether the external effect happened. Treat it as ambiguous even if
			// the same error would be merely transient for a read.
			if candidate.Spec().IsWrite() && decision.Kind == Transient {
				lastKind = Ambiguous
			}
			if lastKind == Ambiguous {
				return Result{ToolID: lastToolID, Attempts: totalAttempts, Fallbacks: candidateIndex, AttemptsLog: records, ErrorKind: lastKind}, lastErr
			}
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
				return Result{ToolID: lastToolID, Attempts: totalAttempts, Fallbacks: candidateIndex, AttemptsLog: records, ErrorKind: lastKind}, ctx.Err()
			}
			if backoff < time.Second {
				backoff *= 2
			}
		}
	}
	return Result{ToolID: lastToolID, Attempts: totalAttempts, Fallbacks: lastFallback, AttemptsLog: records, ErrorKind: lastKind}, lastErr
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
