package doubao

import (
	"encoding/json"

	"github.com/cocoyes/zhizhi-agent-runtime/speech"
)

type sessionEvent struct {
	Type      string         `json:"type"`
	EventID   string         `json:"event_id,omitempty"`
	Session   wireSession    `json:"session"`
	Extension map[string]any `json:"extension,omitempty"`
}

type wireSession struct {
	ID           string              `json:"id,omitempty"`
	Model        string              `json:"model,omitempty"`
	Instructions string              `json:"instructions,omitempty"`
	Audio        *speech.AudioConfig `json:"audio,omitempty"`
	Tools        json.RawMessage     `json:"tools,omitempty"`
}

func newSessionEvent(eventType, eventID string, config speech.SessionConfig) sessionEvent {
	var tools json.RawMessage
	if config.Tools != nil {
		tools, _ = json.Marshal(config.Tools)
	}
	return sessionEvent{
		Type: eventType, EventID: eventID, Extension: config.Extension,
		Session: wireSession{ID: config.ID, Model: config.Model, Instructions: config.Instructions, Audio: config.Audio, Tools: tools},
	}
}

type simpleEvent struct {
	Type    string `json:"type"`
	EventID string `json:"event_id,omitempty"`
}

type audioAppendEvent struct {
	Type    string `json:"type"`
	EventID string `json:"event_id,omitempty"`
	Audio   string `json:"audio"`
}

type speechTextEvent struct {
	Type      string `json:"type"`
	EventID   string `json:"event_id,omitempty"`
	SpeechID  string `json:"speech_id,omitempty"`
	Text      string `json:"text,omitempty"`
	TTSPrompt string `json:"tts_prompt,omitempty"`
}

type itemsEvent struct {
	Type    string                    `json:"type"`
	EventID string                    `json:"event_id,omitempty"`
	Items   []speech.ConversationItem `json:"items"`
}

type wireEvent struct {
	Type       string `json:"type"`
	EventID    string `json:"event_id"`
	ItemID     string `json:"item_id"`
	QuestionID string `json:"question_id"`
	ResponseID string `json:"response_id"`
	Delta      string `json:"delta"`
	Text       string `json:"text"`
	Transcript string `json:"transcript"`
	TTSKind    string `json:"tts_type"`
	StatusCode string `json:"status_code"`
	Session    struct {
		ID string `json:"id"`
	} `json:"session"`
	Items []wireItem            `json:"items"`
	Error *speech.ProviderError `json:"error"`
}

type wireItem struct {
	ID        string           `json:"id,omitempty"`
	Type      string           `json:"type,omitempty"`
	Role      string           `json:"role,omitempty"`
	CallID    string           `json:"call_id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Arguments string           `json:"arguments,omitempty"`
	Status    string           `json:"status,omitempty"`
	Content   []speech.Content `json:"content,omitempty"`
}

func (e wireEvent) ItemsAsCalls() []speech.FunctionCall {
	if e.Type != string(speech.EventFunctionCalls) {
		return nil
	}
	result := make([]speech.FunctionCall, 0, len(e.Items))
	for _, item := range e.Items {
		result = append(result, speech.FunctionCall{CallID: item.CallID, Name: item.Name, Arguments: item.Arguments})
	}
	return result
}

func (e wireEvent) ItemsAsConversation() []speech.ConversationItem {
	if e.Type == string(speech.EventFunctionCalls) {
		return nil
	}
	result := make([]speech.ConversationItem, 0, len(e.Items))
	for _, item := range e.Items {
		result = append(result, speech.ConversationItem{ID: item.ID, Type: item.Type, Role: item.Role, CallID: item.CallID, Status: item.Status, Content: item.Content})
	}
	return result
}
