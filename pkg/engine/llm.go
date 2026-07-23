package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLMClient interacts with a text-to-text generation API (OpenAI compatible)
type LLMClient struct {
	BaseURL string
	client  *http.Client
}

// NewLLMClient creates a new client
func NewLLMClient(baseURL string) *LLMClient {
	return &LLMClient{
		BaseURL: baseURL,
		client: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Messages    []chatMessage `json:"messages"`
	Temperature float32       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

type chatStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
	} `json:"choices"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// Generate responds to a user prompt given a system prompt context
func (c *LLMClient) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	reqBody := chatRequest{
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.1,
		Stream:      true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.BaseURL
	if !strings.HasSuffix(url, "/v1/chat/completions") && !strings.HasSuffix(url, "/completions") {
		url = strings.TrimRight(url, "/") + "/v1/chat/completions"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM server unreachable at %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM server returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Read SSE stream
	scanner := bufio.NewScanner(resp.Body)
	var finalContent strings.Builder
	
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		
		var chunk chatStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err == nil {
			if len(chunk.Choices) > 0 {
				content := chunk.Choices[0].Delta.Content
				if content != "" {
					finalContent.WriteString(content)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading stream: %w", err)
	}

	return finalContent.String(), nil
}
