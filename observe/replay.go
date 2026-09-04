package observe

import (
	"encoding/json"
	"io"
	"time"
)

type Trace struct {
	Events []Event `json:"events"`
}

func (t *Trace) Append(event Event) {
	if event.Time.IsZero() {
		event.Time = time.Unix(0, 0).UTC()
	}
	t.Events = append(t.Events, event)
}
func (t Trace) WriteJSONL(out io.Writer) error {
	enc := json.NewEncoder(out)
	for _, event := range t.Events {
		if err := enc.Encode(event); err != nil {
			return err
		}
	}
	return nil
}
func ReadJSONL(in io.Reader) (Trace, error) {
	dec := json.NewDecoder(in)
	var t Trace
	for {
		var event Event
		if err := dec.Decode(&event); err != nil {
			if err == io.EOF {
				return t, nil
			}
			return Trace{}, err
		}
		t.Events = append(t.Events, event)
	}
}
func (t Trace) Replay(observer Observer) {
	for _, event := range t.Events {
		if observer != nil {
			observer.Observe(event)
		}
	}
}
