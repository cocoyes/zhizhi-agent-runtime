package observe

import (
	"bytes"
	"testing"
)

func TestTraceRoundTrip(t *testing.T) {
	var original Trace
	original.Append(Event{Type: "run.started"})
	var b bytes.Buffer
	if err := original.WriteJSONL(&b); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadJSONL(&b)
	if err != nil || len(loaded.Events) != 1 {
		t.Fatalf("round trip failed: %+v %v", loaded, err)
	}
}
