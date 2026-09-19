package engine

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ProviderKindText   = "TEXT"
	ProviderKindStruct = "STRUCT"
)

// ProviderConfig represents configuration for a single LLM or decision provider.
type ProviderConfig struct {
	Name             string
	Kind             string // "TEXT" or "STRUCT"
	Model            string
	BaseURL          string
	APIKey           string
	ContextSize      int
	Timeout          time.Duration
	Temperature      *float32
	TopP             *float32
	MinP             *float32
	RepeatPenalty    *float32
	PresencePenalty  *float32
	FrequencyPenalty *float32
	ReasoningEffort  string
	RawParams        string
}

// ParseProviderConfig parses provider definition and parameter strings from environment.
func ParseProviderConfig(name, defStr, kindStr, paramsStr string) (*ProviderConfig, error) {
	defStr = strings.TrimSpace(defStr)
	if defStr == "" {
		return nil, fmt.Errorf("provider definition for %s is empty", name)
	}

	parts := strings.Split(defStr, ",")
	model := strings.TrimSpace(parts[0])
	baseURL := ""
	apiKey := ""
	if len(parts) > 1 {
		baseURL = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		apiKey = strings.TrimSpace(parts[2])
	}

	// Resolve API key if not explicit in definition
	if apiKey == "" {
		apiKey = ResolveAPIKey(baseURL, "", strings.ToLower(name))
	}

	// Determine provider kind: defaults to TEXT unless explicitly marked with STRUCT restriction
	kind := strings.ToUpper(strings.TrimSpace(kindStr))
	if kind == ProviderKindStruct || strings.Contains(strings.ToLower(baseURL), "typesafe.ai") || strings.Contains(strings.ToLower(baseURL), "systemone") {
		kind = ProviderKindStruct
	} else {
		kind = ProviderKindText
	}

	cfg := &ProviderConfig{
		Name:      name,
		Kind:      kind,
		Model:     model,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		RawParams: paramsStr,
	}

	// Parse paramsStr (e.g. "131072,timeout:360,temperature:0.3,minp:0.05,topp:0.95,repeat:1.1,presence:0.3,frequency:0.3")
	if paramsStr != "" {
		paramTokens := strings.Split(paramsStr, ",")
		for _, token := range paramTokens {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}

			// Check if token is a plain integer (context size)
			if ctxSize, err := strconv.Atoi(token); err == nil && ctxSize > 0 {
				cfg.ContextSize = ctxSize
				continue
			}

			// Key-value pair
			kv := strings.SplitN(token, ":", 2)
			if len(kv) != 2 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(kv[0]))
			val := strings.TrimSpace(kv[1])

			switch key {
			case "timeout":
				if sec, err := strconv.Atoi(val); err == nil && sec > 0 {
					cfg.Timeout = time.Duration(sec) * time.Second
				}
			case "temperature", "temp":
				if f, err := strconv.ParseFloat(val, 32); err == nil {
					f32 := float32(f)
					cfg.Temperature = &f32
				}
			case "topp", "top_p":
				if f, err := strconv.ParseFloat(val, 32); err == nil {
					f32 := float32(f)
					cfg.TopP = &f32
				}
			case "minp", "min_p":
				if f, err := strconv.ParseFloat(val, 32); err == nil {
					f32 := float32(f)
					cfg.MinP = &f32
				}
			case "repeat", "repeat_penalty", "repetition_penalty":
				if f, err := strconv.ParseFloat(val, 32); err == nil {
					f32 := float32(f)
					cfg.RepeatPenalty = &f32
				}
			case "presence", "presence_penalty":
				if f, err := strconv.ParseFloat(val, 32); err == nil {
					f32 := float32(f)
					cfg.PresencePenalty = &f32
				}
			case "frequency", "frequency_penalty":
				if f, err := strconv.ParseFloat(val, 32); err == nil {
					f32 := float32(f)
					cfg.FrequencyPenalty = &f32
				}
			case "reasoning", "reasoning_effort", "thinking":
				valLower := strings.ToLower(val)
				switch valLower {
				case "off", "false", "no", "0":
					cfg.ReasoningEffort = "none"
				case "on", "true", "yes", "1":
					cfg.ReasoningEffort = "medium"
				default:
					cfg.ReasoningEffort = valLower
				}
			}
		}
	}

	return cfg, nil
}

// ProviderRegistry manages named LLM and System One providers and task routings.
type ProviderRegistry struct {
	mu            sync.RWMutex
	providers     map[string]*ProviderConfig
	textClients   map[string]*LLMClient
	structClients map[string]*TypeSafeClient
	taskRouting   map[string]string
}

// NewProviderRegistry creates an empty registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers:     make(map[string]*ProviderConfig),
		textClients:   make(map[string]*LLMClient),
		structClients: make(map[string]*TypeSafeClient),
		taskRouting:   make(map[string]string),
	}
}

// NewProviderRegistryFromEnv constructs and populates a ProviderRegistry from environment variables.
func NewProviderRegistryFromEnv() *ProviderRegistry {
	r := NewProviderRegistry()

	// 1. Scan well-known providers and any provider with _KIND or _PARAMS in environment
	knownProviders := []string{"TYPESAFE", "CEREBRAS", "OPENROUTER", "LEMOND", "LLAMACPP", "FAST_TEXT_SERVER", "STRONG_TEXT_SERVER"}

	candidateMap := make(map[string]bool)
	for _, p := range knownProviders {
		candidateMap[p] = true
	}

	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		key := pair[0]
		if strings.HasSuffix(key, "_KIND") {
			candidateMap[strings.TrimSuffix(key, "_KIND")] = true
		} else if strings.HasSuffix(key, "_PARAMS") {
			candidateMap[strings.TrimSuffix(key, "_PARAMS")] = true
		}
	}

	for pName := range candidateMap {
		defVal := os.Getenv(pName)
		if defVal == "" {
			continue
		}
		kindVal := os.Getenv(pName + "_KIND")
		paramsVal := os.Getenv(pName + "_PARAMS")

		cfg, err := ParseProviderConfig(pName, defVal, kindVal, paramsVal)
		if err == nil {
			r.RegisterProvider(cfg)
		}
	}

	// 2. Scan standard task routings
	standardTasks := []string{
		"SEMANTIC_EXTRACTION",
		"SEARCH_CANDIDATES",
		"QUERY_DECOMPOSITION",
		"ZERO_SHOT_ANSWER",
		"FINAL_ANSWER",
		"FALLBACK_ANSWER",
		"BENCH_RESULT_EVALUATION",
		"TURN_COMPRESSION",
	}

	for _, task := range standardTasks {
		if val := os.Getenv(task); val != "" {
			r.SetTaskRouting(task, val)
		}
	}

	return r
}

// RegisterProvider registers a provider configuration.
func (r *ProviderRegistry) RegisterProvider(cfg *ProviderConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[strings.ToUpper(cfg.Name)] = cfg
}

// SetTaskRouting maps a task to a provider name.
func (r *ProviderRegistry) SetTaskRouting(taskName, providerName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taskRouting[strings.ToUpper(taskName)] = strings.ToUpper(providerName)
}

// GetProvider returns the configuration for a named provider.
func (r *ProviderRegistry) GetProvider(name string) (*ProviderConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.providers[strings.ToUpper(name)]
	return cfg, ok
}

// GetTextClient returns an initialized LLMClient for a named provider.
func (r *ProviderRegistry) GetTextClient(name string) (*LLMClient, error) {
	name = strings.ToUpper(name)
	r.mu.RLock()
	if c, ok := r.textClients[name]; ok {
		r.mu.RUnlock()
		return c, nil
	}
	cfg, ok := r.providers[name]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("provider %s not found in registry", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Check again under write lock
	if c, ok := r.textClients[name]; ok {
		return c, nil
	}

	client := NewLLMClientWithKey(cfg.BaseURL, cfg.APIKey, cfg.Model)
	if cfg.Timeout > 0 {
		client.TimeoutOverride = &cfg.Timeout
	}
	if cfg.ContextSize > 0 {
		client.ContextOverride = &cfg.ContextSize
	}
	client.TemperatureOverride = cfg.Temperature
	client.TopPOverride = cfg.TopP
	client.MinPOverride = cfg.MinP
	client.RepeatPenaltyOverride = cfg.RepeatPenalty
	client.PresencePenaltyOverride = cfg.PresencePenalty
	client.FrequencyPenaltyOverride = cfg.FrequencyPenalty
	if cfg.ReasoningEffort != "" {
		client.ReasoningEffort = cfg.ReasoningEffort
	}

	r.textClients[name] = client
	return client, nil
}

// GetStructClient returns an initialized TypeSafeClient for a named provider.
func (r *ProviderRegistry) GetStructClient(name string) (*TypeSafeClient, error) {
	name = strings.ToUpper(name)
	r.mu.RLock()
	if c, ok := r.structClients[name]; ok {
		r.mu.RUnlock()
		return c, nil
	}
	cfg, ok := r.providers[name]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("provider %s not found in registry", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.structClients[name]; ok {
		return c, nil
	}

	client := NewTypeSafeClient(cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Timeout)
	r.structClients[name] = client
	return client, nil
}

// GetTextClientForTask returns the appropriate LLMClient for a given task name, or falls back.
func (r *ProviderRegistry) GetTextClientForTask(taskName, fallbackProvider string) (*LLMClient, error) {
	r.mu.RLock()
	providerName, ok := r.taskRouting[strings.ToUpper(taskName)]
	r.mu.RUnlock()

	if !ok || providerName == "" {
		providerName = fallbackProvider
	}

	// Try the mapped provider
	if providerName != "" {
		if client, err := r.GetTextClient(providerName); err == nil {
			return client, nil
		}
	}

	// Try fallback provider if different
	if fallbackProvider != "" && fallbackProvider != providerName {
		if client, err := r.GetTextClient(fallbackProvider); err == nil {
			return client, nil
		}
	}

	// Fallback to any registered text client
	r.mu.RLock()
	defer r.mu.RUnlock()
	for pName, pCfg := range r.providers {
		if pCfg.Kind == ProviderKindText {
			return r.GetTextClient(pName)
		}
	}

	return nil, fmt.Errorf("no text client available for task %s", taskName)
}

// GetStructClientForTask returns the appropriate TypeSafeClient for a given task name.
func (r *ProviderRegistry) GetStructClientForTask(taskName, fallbackProvider string) (*TypeSafeClient, error) {
	r.mu.RLock()
	providerName, ok := r.taskRouting[strings.ToUpper(taskName)]
	r.mu.RUnlock()

	if !ok || providerName == "" {
		providerName = fallbackProvider
	}

	if providerName != "" {
		if client, err := r.GetStructClient(providerName); err == nil {
			return client, nil
		}
	}

	// Fallback to any registered struct client
	r.mu.RLock()
	defer r.mu.RUnlock()
	for pName, pCfg := range r.providers {
		if pCfg.Kind == ProviderKindStruct {
			return r.GetStructClient(pName)
		}
	}

	return nil, fmt.Errorf("no struct client available for task %s", taskName)
}

// TaskRouting returns a copy of current task routing map.
func (r *ProviderRegistry) TaskRouting() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := make(map[string]string, len(r.taskRouting))
	for k, v := range r.taskRouting {
		m[k] = v
	}
	return m
}

// Providers returns a copy of current registered providers map.
func (r *ProviderRegistry) Providers() map[string]*ProviderConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := make(map[string]*ProviderConfig, len(r.providers))
	for k, v := range r.providers {
		m[k] = v
	}
	return m
}
