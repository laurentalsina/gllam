package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// LLMClient interacts with a text-to-text generation API (OpenAI / OpenRouter compatible)
type LLMClient struct {
	BaseURL string
	APIKey  string
	Model   string
	client  *http.Client
}

// NewLLMClient creates a new client with TCP Keep-Alive and infinite streaming timeout
func NewLLMClient(baseURL string) *LLMClient {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	model := os.Getenv("LLM_MODEL")
	if strings.Contains(baseURL, "openrouter.ai") {
		if model == "" || model == "local-server" || model == "local_server" {
			model = "meta-llama/llama-3.3-70b-instruct"
		}
	} else if model == "" {
		model = "local-server"
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	return &LLMClient{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		client: &http.Client{
			Transport: transport,
			Timeout:   0, // Infinite timeout for long-running streaming inference
		},
	}
}

// NewLLMClientWithKey creates a client with explicit API Key and Model ID
func NewLLMClientWithKey(baseURL, apiKey, model string) *LLMClient {
	c := NewLLMClient(baseURL)
	if apiKey != "" {
		c.APIKey = apiKey
	}
	if model != "" {
		c.Model = model
	}
	return c
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model,omitempty"`
	Messages    []chatMessage `json:"messages"`
	Temperature float32       `json:"temperature"`
	Stream      bool          `json:"stream"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type chatStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			Thoughts         string `json:"thoughts"`
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
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.1,
		Stream:      true,
		MaxTokens:   16384,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(url, "/completions") && !strings.HasSuffix(url, "/chat/completions") {
		url = strings.TrimSuffix(url, "/completions") + "/chat/completions"
	} else if !strings.HasSuffix(url, "/chat/completions") {
		if strings.HasSuffix(url, "/v1") {
			url += "/chat/completions"
		} else {
			url += "/v1/chat/completions"
		}
	}

	var resp *http.Response
	var lastErr error

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		if c.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.APIKey)
			req.Header.Set("HTTP-Referer", "https://github.com/laurentalsina/gllam")
			req.Header.Set("X-Title", "GLLAM Memory System")
		}

		resp, err = c.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("LLM server unreachable at %s: %w", url, err)
		} else if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("LLM server returned %d: %s", resp.StatusCode, string(respBody))
		} else {
			lastErr = nil
			break
		}

		if attempt < 3 {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
	}

	if lastErr != nil {
		return "", lastErr
	}
	defer resp.Body.Close()

	// Read SSE stream with expanded buffer for long lines
	scanner := bufio.NewScanner(resp.Body)
	const maxCapacity = 10 * 1024 * 1024
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxCapacity)
	var finalContent strings.Builder
	var finalReasoning strings.Builder

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
				d := chunk.Choices[0].Delta
				if d.Content != "" {
					finalContent.WriteString(d.Content)
				}
				if d.ReasoningContent != "" {
					finalReasoning.WriteString(d.ReasoningContent)
				} else if d.Reasoning != "" {
					finalReasoning.WriteString(d.Reasoning)
				} else if d.Thoughts != "" {
					finalReasoning.WriteString(d.Thoughts)
				}
			}
		}
	}

	resStr := finalContent.String()
	if strings.TrimSpace(resStr) == "" && strings.TrimSpace(finalReasoning.String()) != "" {
		resStr = finalReasoning.String()
	}

	if strings.TrimSpace(resStr) == "" {
		// Fallback to non-streaming request if SSE stream returned empty content (e.g. reasoning token chunks or SSE drops)
		reqBody.Stream = false
		bodyNoStream, errNS := json.Marshal(reqBody)
		if errNS != nil {
			return "", fmt.Errorf("empty streaming response and failed to marshal non-streaming fallback: %w", errNS)
		}
		reqNS, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyNoStream))
		if errReq != nil {
			return "", fmt.Errorf("empty streaming response and failed to create non-streaming request: %w", errReq)
		}
		reqNS.Header.Set("Content-Type", "application/json")
		if c.APIKey != "" {
			reqNS.Header.Set("Authorization", "Bearer "+c.APIKey)
			reqNS.Header.Set("HTTP-Referer", "https://github.com/laurentalsina/gllam")
			reqNS.Header.Set("X-Title", "GLLAM Memory System")
		}
		respNS, errDo := c.client.Do(reqNS)
		if errDo != nil {
			return "", fmt.Errorf("empty streaming response and non-streaming fallback failed: %w", errDo)
		}
		defer respNS.Body.Close()
		respBytes, _ := io.ReadAll(respNS.Body)
		if respNS.StatusCode != http.StatusOK {
			return "", fmt.Errorf("empty streaming response; non-streaming fallback returned %d: %s", respNS.StatusCode, string(respBytes))
		}
		var fullResp chatResponse
		if json.Unmarshal(respBytes, &fullResp) == nil && len(fullResp.Choices) > 0 {
			ans := fullResp.Choices[0].Message.Content
			if strings.TrimSpace(ans) != "" {
				return ans, nil
			}
		}
		return "", fmt.Errorf("LLM returned empty content (raw: %s)", string(respBytes))
	}

	return resStr, nil
}
