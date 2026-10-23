// Package budget implements a request-wide ledger of runtime-visible calls.
package budget

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var Exceeded = errors.New("budget: exceeded")

type Error struct {
	Dimension string
	Limit     int
	Used      int
	Operation string
}

func (e *Error) Error() string {
	return fmt.Sprintf("budget: %s exhausted (limit=%d used=%d operation=%s)", e.Dimension, e.Limit, e.Used, e.Operation)
}
func (e *Error) Unwrap() error { return Exceeded }

type Usage struct {
	ModelCalls int `json:"model_calls"`
	ToolCalls  int `json:"tool_calls"`
}

// Ledger is shared by every execution branch of one request. Zero limits are
// unlimited. Reservations are never refunded after dispatch failures.
type Ledger struct {
	mu          sync.Mutex
	limits      Usage
	used        Usage
	onExhausted func(*Error)
}

func New(maxModelCalls, maxToolCalls int) *Ledger {
	return &Ledger{limits: Usage{maxModelCalls, maxToolCalls}}
}

type contextKey struct{}

func WithLedger(ctx context.Context, l *Ledger) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}
func FromContext(ctx context.Context) *Ledger         { l, _ := ctx.Value(contextKey{}).(*Ledger); return l }
func (l *Ledger) SetOnExhausted(fn func(*Error))      { l.mu.Lock(); l.onExhausted = fn; l.mu.Unlock() }
func (l *Ledger) ReserveModel(operation string) error { return l.reserve(true, operation) }
func (l *Ledger) ReserveTool(operation string) error  { return l.reserve(false, operation) }
func (l *Ledger) reserve(isModel bool, operation string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	used, limit, dimension := &l.used.ToolCalls, l.limits.ToolCalls, "tool_calls"
	if isModel {
		used, limit, dimension = &l.used.ModelCalls, l.limits.ModelCalls, "model_calls"
	}
	if limit > 0 && *used >= limit {
		err := &Error{Dimension: dimension, Limit: limit, Used: *used, Operation: operation}
		callback := l.onExhausted
		l.mu.Unlock()
		if callback != nil {
			callback(err)
		}
		return err
	}
	*used++
	l.mu.Unlock()
	return nil
}
func (l *Ledger) Snapshot() Usage { l.mu.Lock(); defer l.mu.Unlock(); return l.used }

func (l *Ledger) Limits() Usage { l.mu.Lock(); defer l.mu.Unlock(); return l.limits }

// Remaining uses -1 to represent an unlimited dimension.
func (l *Ledger) Remaining() Usage {
	l.mu.Lock()
	defer l.mu.Unlock()
	remaining := func(limit, used int) int {
		if limit <= 0 {
			return -1
		}
		if used >= limit {
			return 0
		}
		return limit - used
	}
	return Usage{remaining(l.limits.ModelCalls, l.used.ModelCalls), remaining(l.limits.ToolCalls, l.used.ToolCalls)}
}

// Restore loads checkpoint consumption before execution begins. Consumption may
// exceed a newly tightened limit, in which case subsequent calls are rejected.
func (l *Ledger) Restore(used Usage) error {
	if used.ModelCalls < 0 || used.ToolCalls < 0 {
		return errors.New("budget: negative checkpoint consumption")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if used.ModelCalls < l.used.ModelCalls || used.ToolCalls < l.used.ToolCalls {
		return errors.New("budget: cannot decrease consumption")
	}
	l.used = used
	return nil
}
