package checkpoint

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreCopiesAndValidates(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	value := Checkpoint{Version: CurrentVersion, ID: "cp-1", RunID: "run-1", CreatedAt: now, UpdatedAt: now}
	if err := store.Save(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.RunID = "changed"
	again, err := store.Load(context.Background(), value.ID)
	if err != nil || again.RunID != "run-1" {
		t.Fatalf("store leaked mutable state: %+v %v", again, err)
	}
	if err := store.Delete(context.Background(), value.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), value.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestCheckpointRejectsIncompatibleVersion(t *testing.T) {
	store := NewMemoryStore()
	err := store.Save(context.Background(), Checkpoint{Version: CurrentVersion + 1, ID: "cp", RunID: "run"})
	if !errors.Is(err, ErrIncompatibleVersion) {
		t.Fatalf("expected incompatible version, got %v", err)
	}
}
