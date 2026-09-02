package observe

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"
)

type Event struct {
	Time        time.Time      `json:"time"`
	Type        string         `json:"type"`
	RunID       string         `json:"run_id,omitempty"`
	PlanVersion int            `json:"plan_version,omitempty"`
	BatchIndex  int            `json:"batch_index,omitempty"`
	StepID      string         `json:"step_id,omitempty"`
	ToolID      string         `json:"tool_id,omitempty"`
	Attempt     int            `json:"attempt,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
}
type Observer interface{ Observe(Event) }
type Func func(Event)

func (f Func) Observe(e Event) { f(e) }

type JSONL struct {
	mu       sync.Mutex
	out      io.Writer
	sanitize bool
}

func NewJSONL(out io.Writer) *JSONL { return &JSONL{out: out, sanitize: true} }
func (o *JSONL) Observe(event Event) {
	if o == nil || o.out == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if o.sanitize {
		event.Data = Sanitize(event.Data)
	}
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = o.out.Write(append(b, '\n'))
}

type Stats struct {
	mu         sync.Mutex
	Events     int
	ModelCalls int
	ToolCalls  int
	Retries    int
	Fallbacks  int
	Replans    int
	Started    time.Time
	Finished   time.Time
}

func (s *Stats) Observe(event Event) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Events++
	switch event.Type {
	case "model.completed":
		s.ModelCalls++
	case "tool.completed":
		s.ToolCalls++
	case "tool.retry":
		s.Retries++
	case "tool.fallback":
		s.Fallbacks++
	case "replan.completed":
		s.Replans++
	case "run.started":
		s.Started = event.Time
	case "run.completed":
		s.Finished = event.Time
	}
}
func (s *Stats) Snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{Events: s.Events, ModelCalls: s.ModelCalls, ToolCalls: s.ToolCalls, Retries: s.Retries, Fallbacks: s.Fallbacks, Replans: s.Replans, Started: s.Started, Finished: s.Finished}
}

func Multi(values ...Observer) Observer {
	return Func(func(e Event) {
		for _, value := range values {
			if value != nil {
				value.Observe(e)
			}
		}
	})
}
func Log(observer Observer, logger *slog.Logger) Observer {
	return Func(func(e Event) {
		if logger != nil {
			logger.Info("agent event", "type", e.Type, "run_id", e.RunID)
		}
		if observer != nil {
			observer.Observe(e)
		}
	})
}

func Sanitize(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for k, v := range values {
		lower := k
		for _, secret := range []string{"key", "token", "secret", "password", "authorization"} {
			if containsFold(lower, secret) {
				out[k] = "[REDACTED]"
				v = nil
				break
			}
		}
		if v != nil {
			if nested, ok := v.(map[string]any); ok {
				v = Sanitize(nested)
			}
			out[k] = v
		}
	}
	return out
}
func containsFold(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		match := true
		for j := range needle {
			a := value[i+j]
			b := needle[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
