package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	CachePrompt    bool                   `json:"cache_prompt,omitempty"`
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
	Tier    string // "strong", "fast", or "default"
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
		Tier:    "default",
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

// GetTimeout returns the configured timeout duration based on the client's Tier and environment variables
func (c *LLMClient) GetTimeout() time.Duration {
	// 1. Check tier-specific environment variables
	if c.Tier == "strong" {
		if tStr := os.Getenv("STRONG_MODEL_TIMEOUT"); tStr != "" {
			if sec, err := strconv.Atoi(tStr); err == nil && sec > 0 {
				return time.Duration(sec) * time.Second
			}
		}
	} else if c.Tier == "fast" {
		if tStr := os.Getenv("FAST_MODEL_TIMEOUT"); tStr != "" {
			if sec, err := strconv.Atoi(tStr); err == nil && sec > 0 {
				return time.Duration(sec) * time.Second
			}
		}
	}

	// 2. Global fallback environment variable
	if tStr := os.Getenv("GLLAM_LLM_TIMEOUT"); tStr != "" {
		if sec, err := strconv.Atoi(tStr); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}

	// 3. Defaults based on tier
	if c.Tier == "strong" {
		return 180 * time.Second
	} else if c.Tier == "fast" {
		return 120 * time.Second
	}

	// Default tier: check both env variables if present
	if tStr := os.Getenv("STRONG_MODEL_TIMEOUT"); tStr != "" {
		if sec, err := strconv.Atoi(tStr); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}
	if tStr := os.Getenv("FAST_MODEL_TIMEOUT"); tStr != "" {
		if sec, err := strconv.Atoi(tStr); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}

	return 180 * time.Second
}

// GetContextSize returns the configured context window token limit based on client Tier and environment variables
func (c *LLMClient) GetContextSize() int {
	if c.Tier == "strong" {
		if cStr := os.Getenv("STRONG_MODEL_CONTEXT"); cStr != "" {
			if tokens, err := strconv.Atoi(cStr); err == nil && tokens > 0 {
				return tokens
			}
		}
		return 131072
	} else if c.Tier == "fast" {
		if cStr := os.Getenv("FAST_MODEL_CONTEXT"); cStr != "" {
			if tokens, err := strconv.Atoi(cStr); err == nil && tokens > 0 {
				return tokens
			}
		}
		return 65536
	}

	// Default tier
	if cStr := os.Getenv("GLLAM_MODEL_CONTEXT"); cStr != "" {
		if tokens, err := strconv.Atoi(cStr); err == nil && tokens > 0 {
			return tokens
		}
	}
	if cStr := os.Getenv("STRONG_MODEL_CONTEXT"); cStr != "" {
		if tokens, err := strconv.Atoi(cStr); err == nil && tokens > 0 {
			return tokens
		}
	}
	if cStr := os.Getenv("FAST_MODEL_CONTEXT"); cStr != "" {
		if tokens, err := strconv.Atoi(cStr); err == nil && tokens > 0 {
			return tokens
		}
	}
	return 65536
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

// GenerateWithFormat sends a completion request with an optional constrained response_format schema, using cache if enabled
func (c *LLMClient) GenerateWithFormat(ctx context.Context, systemPrompt, userPrompt string, responseFormat map[string]interface{}) (string, error) {
	cacheBypass := os.Getenv("GLLAM_BYPASS_CACHE") == "true" || os.Getenv("GLLAM_DISABLE_CACHE") == "true"
	var cacheKey string
	var db *sql.DB
	var dbErr error

	if !cacheBypass {
		db, dbErr = getCacheDB()
		if dbErr == nil {
			cacheKey = computeCacheKey(systemPrompt, userPrompt, responseFormat)
			var cachedResponse string
			var cachedTier string
			err := db.QueryRowContext(ctx, "SELECT response, model_tier FROM prompt_cache WHERE key = ?", cacheKey).Scan(&cachedResponse, &cachedTier)
			if err == nil {
				// Bypass cache if strong client is active but cached response is only from a fast client
				isBypass := c.Tier == "strong" && cachedTier == "fast"
				if !isBypass {
					// Cache hit!
					fmt.Printf("   ├─ [LLM CACHE HIT] model=%s tier=%s key=%s\n", c.Model, c.Tier, cacheKey)
					return cachedResponse, nil
				}
			}
		}
	}

	content, err := c.generateWithFormatNoCache(ctx, systemPrompt, userPrompt, responseFormat)
	if err != nil {
		return "", err
	}

	if !cacheBypass && dbErr == nil && cacheKey != "" && content != "" {
		now := time.Now().UTC().Format(time.RFC3339)
		_, _ = db.ExecContext(ctx, "INSERT OR REPLACE INTO prompt_cache (key, response, model_tier, created_at) VALUES (?, ?, ?, ?)", cacheKey, content, c.Tier, now)
	}

	return content, nil
}

func (c *LLMClient) generateWithFormatNoCache(ctx context.Context, systemPrompt, userPrompt string, responseFormat map[string]interface{}) (string, error) {
	url := c.resolveChatURL()
	timeout := c.GetTimeout()
	ctxSize := c.GetContextSize()

	// Dynamically calculate maxTokens so prompt + maxTokens never exceeds context window
	estimatedPromptTokens := (len(systemPrompt) + len(userPrompt)) / 3
	maxTokens := 16384
	if ctxSize > 0 && estimatedPromptTokens+maxTokens > ctxSize {
		maxTokens = ctxSize - estimatedPromptTokens - 512
		if maxTokens < 2048 {
			maxTokens = 2048
		}
	}

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
			MaxTokens:      maxTokens,
			CachePrompt:    true,
		}

		payload, err := json.Marshal(reqBody)
		if err != nil {
			return "", fmt.Errorf("failed to marshal request: %w", err)
		}

		var reqCtx context.Context
		var cancel context.CancelFunc
		if timeout > 0 {
			reqCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		} else {
			reqCtx = ctx
		}

		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
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
		MaxTokens:   maxTokens,
		CachePrompt: true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	var resp *http.Response
	var lastErr error
	var streamCancel context.CancelFunc

	for attempt := 1; attempt <= 3; attempt++ {
		var reqCtx context.Context
		if timeout > 0 {
			reqCtx, streamCancel = context.WithTimeout(ctx, timeout)
		} else {
			reqCtx = ctx
		}

		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			if streamCancel != nil {
				streamCancel()
			}
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
			if streamCancel != nil {
				streamCancel()
			}
			lastErr = fmt.Errorf("LLM server unreachable at %s: %w", url, err)
		} else if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if streamCancel != nil {
				streamCancel()
			}
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
	defer func() {
		if streamCancel != nil {
			streamCancel()
		}
	}()
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

var (
	cacheDB      *sql.DB
	cacheOnce    sync.Once
	cacheDBErr   error
	cachePath    string
)

func getCacheDB() (*sql.DB, error) {
	cacheOnce.Do(func() {
		dir := os.Getenv("GLLAM_BENCH_DIR")
		if dir == "" {
			dir = "/home/laurent/Projects/gllam/bench"
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			cacheDBErr = err
			return
		}
		cachePath = filepath.Join(dir, "gllam_prompt_cache.db")
		db, err := sql.Open("sqlite3", cachePath)
		if err != nil {
			cacheDBErr = err
			return
		}
		
		pragmas := `
			PRAGMA journal_mode = WAL;
			PRAGMA synchronous = NORMAL;
			PRAGMA busy_timeout = 30000;
		`
		if _, err := db.Exec(pragmas); err != nil {
			db.Close()
			cacheDBErr = err
			return
		}

		createTable := `
			CREATE TABLE IF NOT EXISTS prompt_cache (
				key TEXT PRIMARY KEY,
				response TEXT NOT NULL,
				model_tier TEXT NOT NULL DEFAULT 'default',
				created_at TEXT NOT NULL
			);
		`
		if _, err := db.Exec(createTable); err != nil {
			db.Close()
			cacheDBErr = err
			return
		}

		cacheDB = db
	})

	return cacheDB, cacheDBErr
}

func computeCacheKey(systemPrompt string, userPrompt string, responseFormat map[string]interface{}) string {
	h := sha256.New()
	h.Write([]byte(systemPrompt))
	h.Write([]byte("|"))
	h.Write([]byte(userPrompt))
	if responseFormat != nil {
		h.Write([]byte("|"))
		fmtBytes, _ := json.Marshal(responseFormat)
		h.Write(fmtBytes)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SanitizeJSON cleans LLM markdown codeblocks, non-breaking spaces, and repairs truncated JSON
func SanitizeJSON(s string) string {
	raw := []byte(s)
	raw = bytes.ReplaceAll(raw, []byte{0xc2, 0xa0}, []byte(" "))
	s = string(raw)

	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "```json"); idx != -1 {
		s = s[idx+7:]
	} else if idx := strings.Index(s, "```"); idx != -1 {
		s = s[idx+3:]
	}
	if idx := strings.LastIndex(s, "```"); idx != -1 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)

	start := strings.Index(s, "{")
	if start == -1 {
		return s
	}
	end := strings.LastIndex(s, "}")
	if end != -1 && end > start {
		candidate := s[start : end+1]
		var dummy interface{}
		if json.Unmarshal([]byte(candidate), &dummy) == nil {
			return candidate
		}
	}

	return RepairTruncatedJSON(s[start:])
}

// RepairTruncatedJSON recovers partial JSON outputs that were truncated due to token or context limits
func RepairTruncatedJSON(s string) string {
	var dummy interface{}
	if json.Unmarshal([]byte(s), &dummy) == nil {
		return s
	}

	for i := len(s) - 1; i >= 0; i-- {
		ch := s[i]
		if ch == '}' || ch == ']' || ch == ',' {
			candidate := s[:i]
			if ch != ',' {
				candidate = s[:i+1]
			}

			var stack []rune
			inString := false
			escaped := false
			for _, r := range candidate {
				if escaped {
					escaped = false
					continue
				}
				if r == '\\' {
					escaped = true
					continue
				}
				if r == '"' {
					inString = !inString
					continue
				}
				if inString {
					continue
				}
				if r == '{' || r == '[' {
					stack = append(stack, r)
				} else if r == '}' {
					if len(stack) > 0 && stack[len(stack)-1] == '{' {
						stack = stack[:len(stack)-1]
					}
				} else if r == ']' {
					if len(stack) > 0 && stack[len(stack)-1] == '[' {
						stack = stack[:len(stack)-1]
					}
				}
			}

			if inString {
				candidate += "\""
			}

			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j] == '{' {
					candidate += "}"
				} else if stack[j] == '[' {
					candidate += "]"
				}
			}

			if json.Unmarshal([]byte(candidate), &dummy) == nil {
				return candidate
			}
		}
	}

	return s
}
