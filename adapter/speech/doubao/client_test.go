package doubao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/speech"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
	"github.com/gorilla/websocket"
)

func TestSessionLifecycleAudioAndFunctionCall(t *testing.T) {
	t.Parallel()
	serverErr := make(chan error, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "secret" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		_, create, err := conn.ReadMessage()
		if err != nil {
			serverErr <- err
			return
		}
		var createdRequest map[string]any
		if err := json.Unmarshal(create, &createdRequest); err != nil {
			serverErr <- err
			return
		}
		if createdRequest["type"] != "session.create" {
			serverErr <- &testError{"expected session.create"}
			return
		}
		sessionConfig, ok := createdRequest["session"].(map[string]any)
		if !ok {
			serverErr <- &testError{"expected session config"}
			return
		}
		definitions, ok := sessionConfig["tools"].([]any)
		if !ok || len(definitions) != 1 {
			serverErr <- &testError{"expected one flat tool definition"}
			return
		}
		definition, _ := definitions[0].(map[string]any)
		if definition["name"] != "weather_current" || definition["function"] != nil {
			serverErr <- &testError{"expected normalized flat tool definition"}
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "session.created", "session": map[string]string{"id": "dialog-1"}})
		_ = conn.WriteJSON(map[string]any{"type": "response.function_call_arguments.done", "items": []map[string]string{{"call_id": "call-1", "name": "weather_current", "arguments": `{"city":"深圳"}`}}})

		_, audio, err := conn.ReadMessage()
		if err != nil {
			serverErr <- err
			return
		}
		if !strings.Contains(string(audio), `"type":"input_audio_buffer.append"`) {
			serverErr <- &testError{"expected audio append"}
			return
		}
		_, output, err := conn.ReadMessage()
		if err != nil {
			serverErr <- err
			return
		}
		if !strings.Contains(string(output), `"call_id":"call-1"`) || !strings.Contains(string(output), `深圳`) {
			serverErr <- &testError{"expected matching function output"}
			return
		}
		_, closeFrame, err := conn.ReadMessage()
		if err != nil {
			serverErr <- err
			return
		}
		if !strings.Contains(string(closeFrame), `"type":"session.close"`) {
			serverErr <- &testError{"expected session.close"}
			return
		}
		_ = conn.WriteJSON(map[string]string{"type": "session.closed"})
		serverErr <- nil
	}))
	defer server.Close()

	client, err := New(Config{APIKey: "secret", Endpoint: "ws" + strings.TrimPrefix(server.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	type input struct {
		City string `json:"city" jsonschema:"required"`
	}
	type output struct {
		City string `json:"city"`
	}
	registry := tool.NewRegistry(tool.Func("weather.current", "weather", func(_ context.Context, in input) (output, error) { return output{City: in.City}, nil }))
	tools, err := speech.RegistryToolSet(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultSession("help", "test-voice")
	config.Tools = tools.Definitions
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := client.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if session.ID() != "dialog-1" {
		t.Fatalf("session id = %q", session.ID())
	}
	if err := session.SendAudio(ctx, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if event, err := session.Recv(ctx); err != nil || event.Type != speech.EventSessionCreated {
		t.Fatalf("created event = %#v, %v", event, err)
	}
	event, err := session.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.ExecuteFunctionCalls(ctx, tools.Executor, event.FunctionCalls...); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestDecodeAudioAndProviderError(t *testing.T) {
	event, err := decodeEvent([]byte(`{"type":"response.output_audio.delta","delta":"AQID"}`))
	if err != nil || string(event.Audio) != string([]byte{1, 2, 3}) {
		t.Fatalf("audio event = %#v, %v", event, err)
	}
	event, err = decodeEvent([]byte(`{"type":"error","error":{"code":55000001,"message":"failed"}}`))
	if err != nil || event.ProviderError == nil || !event.ProviderError.Retryable {
		t.Fatalf("error event = %#v, %v", event, err)
	}
}

func TestOpenReturnsProviderSessionError(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteJSON(map[string]any{"type": "error", "error": map[string]string{"code": "45000003", "message": "invalid session"}})
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	client, err := New(Config{APIKey: "secret", Endpoint: "ws" + strings.TrimPrefix(server.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = client.Open(ctx, DefaultSession("help", "voice"))
	var providerErr *speech.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != "45000003" {
		t.Fatalf("open error = %v", err)
	}
}

type testError struct{ value string }

func (e *testError) Error() string { return e.value }
