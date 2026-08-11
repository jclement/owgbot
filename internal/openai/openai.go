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

// Chat runs one completion. gpt-5-family models get minimal reasoning effort
// — these plugins want fast, cheap, terse replies, not deliberation.
func (c *Client) Chat(model string, messages []Message) (string, error) {
	payload := map[string]any{
		"model":    model,
		"messages": messages,
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
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("openai: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("openai: empty response")
	}
	reply := strings.TrimSpace(out.Choices[0].Message.Content)
	if reply == "" {
		return "", fmt.Errorf("openai: empty content")
	}
	return reply, nil
}
