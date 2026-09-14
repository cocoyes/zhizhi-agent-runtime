// Package speech defines provider-neutral contracts for realtime speech sessions.
//
// Realtime speech is intentionally separate from model.Model: a duplex speech
// session is a long-lived, bidirectional transport, while Model is a bounded
// request/response abstraction used by the agent runtime.
package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type EventType string

const (
	EventSessionCreated        EventType = "session.created"
	EventSessionUpdated        EventType = "session.updated"
	EventSessionClosed         EventType = "session.closed"
	EventAudioCommitted        EventType = "input_audio_buffer.committed"
	EventTranscriptionStarted  EventType = "conversation.item.input_audio_transcription.started"
	EventTranscriptionDelta    EventType = "conversation.item.input_audio_transcription.delta"
	EventTranscriptionDone     EventType = "conversation.item.input_audio_transcription.completed"
	EventTranscriptionFailed   EventType = "conversation.item.input_audio_transcription.failed"
	EventTextDelta             EventType = "response.output_text.delta"
	EventTextDone              EventType = "response.output_text.done"
	EventAudioStarted          EventType = "response.output_audio.started"
	EventAudioDelta            EventType = "response.output_audio.delta"
	EventAudioDone             EventType = "response.output_audio.done"
	EventFunctionCalls         EventType = "response.function_call_arguments.done"
	EventConversationAdded     EventType = "conversation.item.added"
	EventConversationUpdated   EventType = "conversation.item.updated"
	EventConversationRetrieved EventType = "conversation.item.retrieved"
	EventConversationDeleted   EventType = "conversation.item.deleted"
	EventResponseCanceled      EventType = "response.canceled"
	EventResponseDone          EventType = "response.done"
	EventError                 EventType = "error"
)

type AudioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate"`
}

type AudioConfig struct {
	Input  AudioInput  `json:"input"`
	Output AudioOutput `json:"output"`
}

type AudioInput struct {
	Format AudioFormat `json:"format"`
}

type AudioOutput struct {
	Format   AudioFormat `json:"format"`
	Voice    string      `json:"voice,omitempty"`
	Speed    int         `json:"speed,omitempty"`
	Loudness int         `json:"loudness,omitempty"`
}

type ToolDefinition struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type SessionConfig struct {
	ID           string           `json:"id,omitempty"`
	Model        string           `json:"model,omitempty"`
	Instructions string           `json:"instructions,omitempty"`
	Audio        *AudioConfig     `json:"audio,omitempty"`
	Tools        []ToolDefinition `json:"tools,omitempty"`
	// Extension is provider-owned configuration. The Doubao adapter forwards
	// extension.asr/tts/dialog without interpreting it.
	Extension map[string]any `json:"-"`
}

type FunctionCall struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type FunctionOutput struct {
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ConversationItem struct {
	ID      string    `json:"id,omitempty"`
	Type    string    `json:"type,omitempty"`
	Role    string    `json:"role,omitempty"`
	CallID  string    `json:"call_id,omitempty"`
	Status  string    `json:"status,omitempty"`
	Content []Content `json:"content,omitempty"`
}

type ProviderError struct {
	Type      string `json:"type,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	EventID   string `json:"event_id,omitempty"`
	Retryable bool   `json:"-"`
}

// UnmarshalJSON accepts both string and numeric provider error codes.
func (e *ProviderError) UnmarshalJSON(data []byte) error {
	var value struct {
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Message string `json:"message"`
		EventID string `json:"event_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	e.Type, e.Message, e.EventID = value.Type, value.Message, value.EventID
	if value.Code != nil {
		e.Code = fmt.Sprint(value.Code)
	}
	return nil
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "speech provider error"
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type Event struct {
	Type          EventType          `json:"type"`
	EventID       string             `json:"event_id,omitempty"`
	SessionID     string             `json:"session_id,omitempty"`
	ItemID        string             `json:"item_id,omitempty"`
	QuestionID    string             `json:"question_id,omitempty"`
	ResponseID    string             `json:"response_id,omitempty"`
	Delta         string             `json:"delta,omitempty"`
	Text          string             `json:"text,omitempty"`
	Transcript    string             `json:"transcript,omitempty"`
	Audio         []byte             `json:"audio,omitempty"`
	TTSKind       string             `json:"tts_type,omitempty"`
	StatusCode    string             `json:"status_code,omitempty"`
	FunctionCalls []FunctionCall     `json:"function_calls,omitempty"`
	Items         []ConversationItem `json:"items,omitempty"`
	ProviderError *ProviderError     `json:"error,omitempty"`
	Raw           json.RawMessage    `json:"-"`
}

// ToolExecutor executes a provider-requested function. The returned bytes must
// be valid JSON; adapters serialize them as a string in the tool result item.
type ToolExecutor interface {
	Execute(context.Context, FunctionCall) (json.RawMessage, error)
}

type ToolExecutorFunc func(context.Context, FunctionCall) (json.RawMessage, error)

func (f ToolExecutorFunc) Execute(ctx context.Context, call FunctionCall) (json.RawMessage, error) {
	return f(ctx, call)
}

type Session interface {
	ID() string
	SendAudio(context.Context, []byte) error
	CommitAudio(context.Context) error
	SetMuted(context.Context, bool) error
	Say(context.Context, string) error
	ReplaceSpeech(context.Context, string) error
	CreateItems(context.Context, ...ConversationItem) error
	UpdateItems(context.Context, ...ConversationItem) error
	RetrieveItems(context.Context, ...ConversationItem) error
	DeleteItems(context.Context, ...ConversationItem) error
	Update(context.Context, SessionConfig) error
	CancelResponse(context.Context) error
	Recv(context.Context) (Event, error)
	SubmitFunctionOutputs(context.Context, ...FunctionOutput) error
	ExecuteFunctionCalls(context.Context, ToolExecutor, ...FunctionCall) error
	Close(context.Context) error
}

type Client interface {
	Open(context.Context, SessionConfig) (Session, error)
}

var ErrClosed = errors.New("speech: session closed")

func IsEnd(err error) bool { return errors.Is(err, io.EOF) || errors.Is(err, ErrClosed) }
