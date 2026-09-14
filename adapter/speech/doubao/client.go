// Package doubao implements Volcengine Doubao's JSON WebSocket realtime speech
// protocol.
package doubao

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/speech"
	"github.com/gorilla/websocket"
)

const (
	DefaultEndpoint = "wss://openspeech.bytedance.com/api/v3/duplex/realtime/dialogue"
	DefaultModel    = "1.2.6.1"
	AuthXAPIKey     = "x-api-key"
	AuthBearer      = "bearer"

	AudioPCM        = "pcm"
	AudioPCMS16LE   = "pcm_s16le"
	AudioOggOpus    = "ogg_opus"
	AudioSpeechOpus = "speech_opus"
)

type Config struct {
	APIKey           string
	AuthType         string
	Endpoint         string
	HandshakeTimeout time.Duration
	WriteTimeout     time.Duration
	CloseTimeout     time.Duration
	ReadBufferSize   int
	WriteBufferSize  int
	EventBufferSize  int
	MaxMessageBytes  int64
	Header           http.Header
}

type Client struct {
	config Config
	dialer *websocket.Dialer
}

// DefaultSession returns the recommended browser/backend bridge configuration:
// 16 kHz mono PCM input and 24 kHz mono signed-16-bit PCM output.
func DefaultSession(instructions, voice string) speech.SessionConfig {
	return speech.SessionConfig{
		Model: DefaultModel, Instructions: instructions,
		Audio: &speech.AudioConfig{
			Input:  speech.AudioInput{Format: speech.AudioFormat{Type: AudioPCM, Rate: 16000}},
			Output: speech.AudioOutput{Format: speech.AudioFormat{Type: AudioPCMS16LE, Rate: 24000}, Voice: voice},
		},
	}
}

func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("doubao realtime speech: api key is required")
	}
	if config.Endpoint == "" {
		config.Endpoint = DefaultEndpoint
	}
	if config.AuthType == "" {
		config.AuthType = AuthXAPIKey
	}
	if config.AuthType != AuthXAPIKey && config.AuthType != AuthBearer {
		return nil, fmt.Errorf("doubao realtime speech: unsupported auth type %q", config.AuthType)
	}
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = 10 * time.Second
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 10 * time.Second
	}
	if config.CloseTimeout <= 0 {
		config.CloseTimeout = 5 * time.Second
	}
	if config.EventBufferSize <= 0 {
		config.EventBufferSize = 128
	}
	if config.MaxMessageBytes <= 0 {
		config.MaxMessageBytes = 8 << 20
	}
	return &Client{config: config, dialer: &websocket.Dialer{
		HandshakeTimeout: config.HandshakeTimeout,
		ReadBufferSize:   config.ReadBufferSize,
		WriteBufferSize:  config.WriteBufferSize,
	}}, nil
}

func (c *Client) Open(ctx context.Context, config speech.SessionConfig) (speech.Session, error) {
	if c == nil {
		return nil, fmt.Errorf("doubao realtime speech: nil client")
	}
	if config.Model == "" {
		config.Model = DefaultModel
	}
	if err := validateSessionConfig(config); err != nil {
		return nil, err
	}
	header := c.config.Header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	if c.config.AuthType == AuthBearer {
		header.Set("Authorization", "Bearer "+c.config.APIKey)
	} else {
		header.Set("X-Api-Key", c.config.APIKey)
	}
	conn, response, err := c.dialer.DialContext(ctx, c.config.Endpoint, header)
	if err != nil {
		logID := ""
		if response != nil {
			logID = response.Header.Get("X-Tt-Logid")
		}
		if logID != "" {
			return nil, fmt.Errorf("doubao realtime speech: websocket dial failed (log_id=%s): %w", logID, err)
		}
		return nil, fmt.Errorf("doubao realtime speech: websocket dial failed: %w", err)
	}
	conn.SetReadLimit(c.config.MaxMessageBytes)
	sessionCtx, cancel := context.WithCancel(ctx)
	s := &session{
		conn: conn, config: c.config, ctx: sessionCtx, cancel: cancel,
		events:  make(chan speech.Event, c.config.EventBufferSize),
		created: make(chan struct{}), startupErr: make(chan error, 1), closed: make(chan struct{}), readerDone: make(chan struct{}),
	}
	go s.readLoop()
	if err := s.send(ctx, newSessionEvent("session.create", s.nextEventID(), config)); err != nil {
		s.abort()
		return nil, err
	}
	select {
	case <-s.created:
		return s, nil
	case err := <-s.startupErr:
		s.abort()
		return nil, fmt.Errorf("doubao realtime speech: session.create: %w", err)
	case <-s.readerDone:
		err := s.readerError()
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		s.abort()
		return nil, fmt.Errorf("doubao realtime speech: session.create: %w", err)
	case <-ctx.Done():
		s.abort()
		return nil, ctx.Err()
	}
}

func validateSessionConfig(config speech.SessionConfig) error {
	if config.Audio == nil || config.Audio.Input.Format.Type == "" || config.Audio.Input.Format.Rate <= 0 {
		return fmt.Errorf("doubao realtime speech: input audio format and rate are required")
	}
	if config.Audio.Output.Format.Type == "" || config.Audio.Output.Format.Rate <= 0 {
		return fmt.Errorf("doubao realtime speech: output audio format and rate are required")
	}
	for _, value := range config.Tools {
		if value.Type != "function" || value.Name == "" || (len(value.Parameters) > 0 && !json.Valid(value.Parameters)) {
			return fmt.Errorf("doubao realtime speech: invalid function tool %q", value.Name)
		}
	}
	return nil
}

type session struct {
	conn   *websocket.Conn
	config Config
	ctx    context.Context
	cancel context.CancelFunc

	writeMu sync.Mutex
	stateMu sync.RWMutex
	id      string
	readErr error

	events     chan speech.Event
	created    chan struct{}
	startupErr chan error
	createdOne sync.Once
	closed     chan struct{}
	closedOne  sync.Once
	readerDone chan struct{}
	closeOne   sync.Once
	closeErr   error
	eventSeq   atomic.Uint64
}

func (s *session) ID() string {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.id
}

func (s *session) nextEventID() string {
	return fmt.Sprintf("event_%d", s.eventSeq.Add(1))
}

func (s *session) SendAudio(ctx context.Context, audio []byte) error {
	if len(audio) == 0 {
		return fmt.Errorf("doubao realtime speech: empty audio frame")
	}
	return s.send(ctx, audioAppendEvent{Type: "input_audio_buffer.append", EventID: s.nextEventID(), Audio: base64.StdEncoding.EncodeToString(audio)})
}

func (s *session) CommitAudio(ctx context.Context) error {
	return s.sendSimple(ctx, "input_audio_buffer.commit")
}

func (s *session) SetMuted(ctx context.Context, muted bool) error {
	eventType := "input_audio_unmute.commit"
	if muted {
		eventType = "input_audio_mute.commit"
	}
	return s.sendSimple(ctx, eventType)
}

func (s *session) Say(ctx context.Context, text string) error {
	if text == "" {
		return fmt.Errorf("doubao realtime speech: speech text is required")
	}
	return s.send(ctx, speechTextEvent{Type: "speech_text_buffer.commit", EventID: s.nextEventID(), SpeechID: s.nextEventID(), Text: text})
}

func (s *session) ReplaceSpeech(ctx context.Context, text string) error {
	if text == "" {
		return fmt.Errorf("doubao realtime speech: replacement text is required")
	}
	return s.send(ctx, speechTextEvent{Type: "speech_text_buffer.replacement.commit", EventID: s.nextEventID(), SpeechID: s.nextEventID(), Text: text})
}

func (s *session) CreateItems(ctx context.Context, items ...speech.ConversationItem) error {
	return s.sendItems(ctx, "conversation.item.create", items)
}

func (s *session) UpdateItems(ctx context.Context, items ...speech.ConversationItem) error {
	return s.sendItems(ctx, "conversation.item.update", items)
}

func (s *session) RetrieveItems(ctx context.Context, items ...speech.ConversationItem) error {
	return s.sendItems(ctx, "conversation.item.retrieve", items)
}

func (s *session) DeleteItems(ctx context.Context, items ...speech.ConversationItem) error {
	return s.sendItems(ctx, "conversation.item.delete", items)
}

func (s *session) sendItems(ctx context.Context, eventType string, items []speech.ConversationItem) error {
	if len(items) == 0 {
		return fmt.Errorf("doubao realtime speech: conversation items are required")
	}
	return s.send(ctx, itemsEvent{Type: eventType, EventID: s.nextEventID(), Items: items})
}

func (s *session) Update(ctx context.Context, config speech.SessionConfig) error {
	if config.ID == "" {
		config.ID = s.ID()
	}
	return s.send(ctx, newSessionEvent("session.update", s.nextEventID(), config))
}

func (s *session) CancelResponse(ctx context.Context) error {
	return s.sendSimple(ctx, "response.cancel")
}

func (s *session) Recv(ctx context.Context) (speech.Event, error) {
	select {
	case event, ok := <-s.events:
		if !ok {
			if err := s.readerError(); err != nil {
				return speech.Event{}, err
			}
			return speech.Event{}, io.EOF
		}
		return event, nil
	case <-ctx.Done():
		return speech.Event{}, ctx.Err()
	}
}

func (s *session) SubmitFunctionOutputs(ctx context.Context, outputs ...speech.FunctionOutput) error {
	if len(outputs) == 0 {
		return fmt.Errorf("doubao realtime speech: function outputs are required")
	}
	items := make([]speech.ConversationItem, len(outputs))
	for i, output := range outputs {
		if output.CallID == "" {
			return fmt.Errorf("doubao realtime speech: function output call_id is required")
		}
		items[i] = speech.ConversationItem{Type: "message", Role: "tool", CallID: output.CallID, Content: []speech.Content{{Type: "input_text", Text: output.Output}}}
	}
	return s.CreateItems(ctx, items...)
}

func (s *session) ExecuteFunctionCalls(ctx context.Context, executor speech.ToolExecutor, calls ...speech.FunctionCall) error {
	if executor == nil {
		return fmt.Errorf("doubao realtime speech: tool executor is required")
	}
	if len(calls) == 0 {
		return nil
	}
	for _, call := range calls {
		if call.CallID == "" || call.Name == "" {
			return fmt.Errorf("doubao realtime speech: function call name and call_id are required")
		}
	}
	outputs := make([]speech.FunctionOutput, len(calls))
	errs := make([]error, len(calls))
	var group sync.WaitGroup
	for i, call := range calls {
		i, call := i, call
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := executor.Execute(ctx, call)
			if err != nil {
				errs[i] = fmt.Errorf("tool %s (%s): %w", call.Name, call.CallID, err)
				result, _ = json.Marshal(map[string]string{"error": err.Error()})
			}
			if len(result) == 0 || !json.Valid(result) {
				errs[i] = errors.Join(errs[i], fmt.Errorf("tool %s (%s) returned invalid JSON", call.Name, call.CallID))
				result = json.RawMessage(`{"error":"tool returned invalid JSON"}`)
			}
			outputs[i] = speech.FunctionOutput{CallID: call.CallID, Output: string(result)}
		}()
	}
	group.Wait()
	if err := s.SubmitFunctionOutputs(ctx, outputs...); err != nil {
		return errors.Join(append(errs, err)...)
	}
	return errors.Join(errs...)
}

func (s *session) Close(ctx context.Context) error {
	s.closeOne.Do(func() {
		closeCtx := ctx
		cancel := func() {}
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			closeCtx, cancel = context.WithTimeout(ctx, s.config.CloseTimeout)
		}
		defer cancel()
		if err := s.sendSimple(closeCtx, "session.close"); err != nil {
			s.closeErr = err
			s.abort()
			return
		}
		select {
		case <-s.closed:
		case <-s.readerDone:
			s.closeErr = s.readerError()
		case <-closeCtx.Done():
			s.closeErr = closeCtx.Err()
		}
		s.abort()
	})
	return s.closeErr
}

func (s *session) sendSimple(ctx context.Context, eventType string) error {
	return s.send(ctx, simpleEvent{Type: eventType, EventID: s.nextEventID()})
}

func (s *session) send(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("doubao realtime speech: marshal event: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	select {
	case <-s.readerDone:
		return speech.ErrClosed
	default:
	}
	deadline := time.Now().Add(s.config.WriteTimeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	if err := s.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if err := s.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return fmt.Errorf("doubao realtime speech: write event: %w", err)
	}
	return nil
}

func (s *session) abort() {
	s.cancel()
	_ = s.conn.Close()
}

func (s *session) readerError() error {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.readErr
}

func (s *session) setReaderError(err error) {
	s.stateMu.Lock()
	s.readErr = err
	s.stateMu.Unlock()
}

func (s *session) readLoop() {
	defer close(s.readerDone)
	defer close(s.events)
	for {
		messageType, frame, err := s.conn.ReadMessage()
		if err != nil {
			if s.ctx.Err() == nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.setReaderError(fmt.Errorf("doubao realtime speech: read event: %w", err))
			}
			return
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		event, err := decodeEvent(frame)
		if err != nil {
			s.setReaderError(err)
			return
		}
		if event.SessionID != "" {
			s.stateMu.Lock()
			s.id = event.SessionID
			s.stateMu.Unlock()
		}
		switch event.Type {
		case speech.EventSessionCreated:
			s.createdOne.Do(func() { close(s.created) })
		case speech.EventSessionClosed:
			s.closedOne.Do(func() { close(s.closed) })
		case speech.EventError:
			select {
			case <-s.created:
			default:
				if event.ProviderError != nil {
					select {
					case s.startupErr <- event.ProviderError:
					default:
					}
				}
			}
		}
		select {
		case s.events <- event:
		case <-s.ctx.Done():
			return
		}
	}
}

func decodeEvent(frame []byte) (speech.Event, error) {
	var wire wireEvent
	if err := json.Unmarshal(frame, &wire); err != nil {
		return speech.Event{}, fmt.Errorf("doubao realtime speech: decode event: %w", err)
	}
	if wire.Type == "" {
		return speech.Event{}, fmt.Errorf("doubao realtime speech: event type is missing")
	}
	event := speech.Event{
		Type: speech.EventType(wire.Type), EventID: wire.EventID, SessionID: wire.Session.ID,
		ItemID: wire.ItemID, QuestionID: wire.QuestionID, ResponseID: wire.ResponseID,
		Delta: wire.Delta, Text: wire.Text, Transcript: wire.Transcript,
		TTSKind: wire.TTSKind, StatusCode: wire.StatusCode,
		FunctionCalls: wire.ItemsAsCalls(), Items: wire.ItemsAsConversation(), Raw: append(json.RawMessage(nil), frame...),
	}
	if wire.Type == string(speech.EventAudioDelta) {
		audio, err := base64.StdEncoding.DecodeString(wire.Delta)
		if err != nil {
			return speech.Event{}, fmt.Errorf("doubao realtime speech: decode audio delta: %w", err)
		}
		event.Audio = audio
	}
	if wire.Error != nil {
		wire.Error.Retryable = strings.HasPrefix(wire.Error.Code, "5")
		event.ProviderError = wire.Error
	}
	return event, nil
}

var _ speech.Client = (*Client)(nil)
var _ speech.Session = (*session)(nil)
