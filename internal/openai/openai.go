// Package openai is a minimal chat-completions client shared by the ai and
// zork plugins.
package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is set on assistant messages that invoke tools.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a role:"tool" result message to its call.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall is one function invocation requested by the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool declares a function the model may call.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Client talks to the OpenAI chat-completions API.
type Client struct {
	Key string
	// BaseURL overrides the API endpoint (tests).
	BaseURL string
	HTTP    *http.Client
}

func New(key string) *Client {
	return &Client{
		Key:  key,
		HTTP: &http.Client{Timeout: 60 * time.Second},
	}
}

// Chat runs one completion and returns the reply text.
func (c *Client) Chat(model string, messages []Message) (string, error) {
	m, err := c.ChatTools(model, messages, nil)
	if err != nil {
		return "", err
	}
	reply := strings.TrimSpace(m.Content)
	if reply == "" {
		return "", fmt.Errorf("openai: empty content")
	}
	return reply, nil
}

// ChatTools runs one completion with optional tool definitions and returns
// the full assistant message (which may carry tool calls instead of text).
// gpt-5-family models get minimal reasoning effort — these plugins want
// fast, cheap, terse replies, not deliberation.
func (c *Client) ChatTools(model string, messages []Message, tools []Tool) (Message, error) {
	payload := map[string]any{
		"model":    model,
		"messages": messages,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	if strings.HasPrefix(model, "gpt-5") {
		payload["reasoning_effort"] = "minimal"
	}
	body, _ := json.Marshal(payload)

	base := c.BaseURL
	if base == "" {
		base = "https://api.openai.com"
	}
	req, err := http.NewRequest("POST", base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Message{}, err
	}
	if out.Error != nil {
		return Message{}, fmt.Errorf("openai: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return Message{}, fmt.Errorf("openai: empty response")
	}
	return out.Choices[0].Message, nil
}
