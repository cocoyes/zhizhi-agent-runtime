package budget

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentReservationAndRestore(t *testing.T) {
	l := New(1, 1)
	var passed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.ReserveTool("dispatch") == nil {
				passed.Add(1)
			}
		}()
	}
	wg.Wait()
	if passed.Load() != 1 {
		t.Fatalf("dispatches = %d", passed.Load())
	}
	var exceeded *Error
	if err := l.ReserveTool("retry"); !errors.Is(err, Exceeded) || !errors.As(err, &exceeded) || exceeded.Used != 1 {
		t.Fatalf("error = %v", err)
	}
	restored := New(1, 1)
	if err := restored.Restore(l.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := restored.ReserveTool("resume"); !errors.Is(err, Exceeded) {
		t.Fatalf("restore bypassed limit: %v", err)
	}
	if got := New(0, 0).Remaining(); got.ModelCalls != -1 || got.ToolCalls != -1 {
		t.Fatalf("unlimited = %+v", got)
	}
}
