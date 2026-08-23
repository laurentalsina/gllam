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

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionRequest struct {
	Model          string                 `json:"model,omitempty"`
	Messages       []ChatMessage          `json:"messages"`
	ResponseFormat map[string]interface{} `json:"response_format,omitempty"`
	Temperature    float32                `json:"temperature"`
	Stream         bool                   `json:"stream"`
	MaxTokens      int                    `json:"max_tokens,omitempty"`
}

type ChatStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			Thoughts         string `json:"thoughts"`
		} `json:"delta"`
	} `json:"choices"`
}

type ChatCompletionResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
}

// LLMClient interacts with a text-to-text generation API (OpenAI / OpenRouter / llama.cpp compatible)
type LLMClient struct {
	BaseURL string
	APIKey  string
	Model   string
	client  *http.Client
}

// NewLLMClient creates a new client with TCP Keep-Alive and infinite streaming timeout
func NewLLMClient(baseURL string) *LLMClient {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("LLM_API_KEY")
	}

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

// resolveChatURL standardizes the base URL into a chat completions endpoint
func (c *LLMClient) resolveChatURL() string {
	url := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(url, "/completions") && !strings.HasSuffix(url, "/chat/completions") {
		return strings.TrimSuffix(url, "/completions") + "/chat/completions"
	} else if !strings.HasSuffix(url, "/chat/completions") {
		if strings.HasSuffix(url, "/v1") {
			return url + "/chat/completions"
		}
		return url + "/v1/chat/completions"
	}
	return url
}

// Generate responds to a user prompt given a system prompt context
func (c *LLMClient) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return c.GenerateWithFormat(ctx, systemPrompt, userPrompt, nil)
}

// adaptResponseFormat translates the custom llama-server JSON Schema format into standard OpenAI/OpenRouter format when using remote endpoints
func (c *LLMClient) adaptResponseFormat(original map[string]interface{}) map[string]interface{} {
	if original == nil {
		return nil
	}

	// Check if this is OpenRouter/OpenAI-compatible and needs standard JSON Schema format
	isLocal := strings.Contains(c.BaseURL, "127.0.0.1") || strings.Contains(c.BaseURL, "localhost") || strings.Contains(c.BaseURL, "100.96.")

	t, okType := original["type"].(string)
	schema, okSchema := original["schema"]

	if !isLocal && okType && t == "json_object" && okSchema {
		// Convert to standard OpenAI / OpenRouter json_schema format
		return map[string]interface{}{
			"type": "json_schema",
			"json_schema": map[string]interface{}{
				"name":   "semantic_extraction",
				"strict": true,
				"schema": schema,
			},
		}
	}

	return original
}

// GenerateWithFormat sends a completion request with an optional constrained response_format schema
func (c *LLMClient) GenerateWithFormat(ctx context.Context, systemPrompt, userPrompt string, responseFormat map[string]interface{}) (string, error) {
	url := c.resolveChatURL()

	// If response format is provided, perform a non-streaming constrained request
	if responseFormat != nil {
		reqBody := ChatCompletionRequest{
			Model: c.Model,
			Messages: []ChatMessage{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: userPrompt},
			},
			ResponseFormat: c.adaptResponseFormat(responseFormat),
			Temperature:    0.1,
			Stream:         false,
			MaxTokens:      16384,
		}

		payload, err := json.Marshal(reqBody)
		if err != nil {
			return "", fmt.Errorf("failed to marshal request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.APIKey)
			req.Header.Set("HTTP-Referer", "https://github.com/laurentalsina/gllam")
			req.Header.Set("X-Title", "GLLAM Memory System")
		}

		resp, err := c.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return "", fmt.Errorf("llm server returned status %d: %s", resp.StatusCode, string(bodyBytes))
		}

		var chatResp ChatCompletionResponse
		if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
			return "", fmt.Errorf("failed to decode response: %w", err)
		}

		if len(chatResp.Choices) == 0 {
			return "", fmt.Errorf("no completion choices returned")
		}

		content := chatResp.Choices[0].Message.Content
		if strings.TrimSpace(content) == "" {
			return "", fmt.Errorf("llm server returned an empty completion content choice (possibly blocked by content filter or context limit)")
		}

		return content, nil
	}

	// Default: Streaming generation with automatic non-streaming fallback
	reqBody := ChatCompletionRequest{
		Model: c.Model,
		Messages: []ChatMessage{
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

	scanner := bufio.NewScanner(resp.Body)
	const maxCapacity = 10 * 1024 * 1024
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxCapacity)
	var finalContent strings.Builder
	var finalReasoning strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		//fmt.Println("RAW LINE:", line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk ChatStreamResponse
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
		var fullResp ChatCompletionResponse
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
