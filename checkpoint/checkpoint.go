// Package checkpoint defines durable, provider-neutral suspended-run state.
package checkpoint

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

const CurrentVersion = 1

var (
	ErrNotFound            = errors.New("checkpoint: not found")
	ErrIncompatibleVersion = errors.New("checkpoint: incompatible version")
	ErrCatalogDrift        = errors.New("checkpoint: tool catalog drift")
)

type SuspendReason string

const (
	ReasonConfirmation SuspendReason = "confirmation_required"
	ReasonInterrupted  SuspendReason = "interrupted"
)

type BudgetUsage struct {
	ModelCalls int `json:"model_calls"`
	ToolCalls  int `json:"tool_calls"`
	Replans    int `json:"replans"`
}

type PendingInvocation struct {
	CallID    string          `json:"call_id"`
	ToolID    string          `json:"tool_id"`
	Arguments json.RawMessage `json:"arguments"`
	StepID    string          `json:"step_id,omitempty"`
}

// Checkpoint contains stable common fields plus opaque runtime State. Keeping
// State opaque lets the executor evolve without coupling stores to internals.
type Checkpoint struct {
	Version            int                      `json:"version"`
	ID                 string                   `json:"id"`
	RunID              string                   `json:"run_id"`
	Reason             SuspendReason            `json:"reason"`
	CatalogFingerprint string                   `json:"catalog_fingerprint"`
	Messages           []model.Message          `json:"messages,omitempty"`
	Evidence           []contract.Evidence      `json:"evidence,omitempty"`
	Actions            []contract.ActionReceipt `json:"actions,omitempty"`
	Pending            *PendingInvocation       `json:"pending,omitempty"`
	Budget             BudgetUsage              `json:"budget"`
	State              json.RawMessage          `json:"state,omitempty"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
}

func (c Checkpoint) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrIncompatibleVersion, c.Version, CurrentVersion)
	}
	if c.ID == "" || c.RunID == "" {
		return errors.New("checkpoint: id and run id are required")
	}
	return nil
}

type Store interface {
	Save(context.Context, Checkpoint) error
	Load(context.Context, string) (*Checkpoint, error)
	Delete(context.Context, string) error
}

// CheckpointStore is the descriptive public alias retained for API discovery.
type CheckpointStore = Store

// MemoryStore is concurrency safe and useful for tests or a single process.
// Production applications should inject a durable Store.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string][]byte
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{items: make(map[string][]byte)} }

func (s *MemoryStore) Save(ctx context.Context, value Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("checkpoint: encode: %w", err)
	}
	s.mu.Lock()
	s.items[value.ID] = encoded
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Load(ctx context.Context, id string) (*Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	encoded, ok := s.items[id]
	copyBytes := append([]byte(nil), encoded...)
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	var value Checkpoint
	if err := json.Unmarshal(copyBytes, &value); err != nil {
		return nil, fmt.Errorf("checkpoint: decode: %w", err)
	}
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return &value, nil
}

func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return ErrNotFound
	}
	delete(s.items, id)
	return nil
}

func NewID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("checkpoint: generate id: %w", err)
	}
	return "cp-" + hex.EncodeToString(data[:]), nil
}
