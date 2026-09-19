package engine

import (
	"os"
	"testing"
	"time"
)

func TestParseProviderConfig(t *testing.T) {
	// 1. STRUCT provider (TypeSafe / Jev)
	typesafeDef := "Jev,https://api.typesafe.ai/v1/systemone,test_key_123"
	typesafeParams := "65535"
	cfg, err := ParseProviderConfig("TYPESAFE", typesafeDef, "STRUCT", typesafeParams)
	if err != nil {
		t.Fatalf("Failed to parse TYPESAFE config: %v", err)
	}
	if cfg.Kind != ProviderKindStruct {
		t.Errorf("Expected kind STRUCT, got %s", cfg.Kind)
	}
	if cfg.Model != "Jev" {
		t.Errorf("Expected model Jev, got %s", cfg.Model)
	}
	if cfg.BaseURL != "https://api.typesafe.ai/v1/systemone" {
		t.Errorf("Expected BaseURL https://api.typesafe.ai/v1/systemone, got %s", cfg.BaseURL)
	}
	if cfg.APIKey != "test_key_123" {
		t.Errorf("Expected APIKey test_key_123, got %s", cfg.APIKey)
	}
	if cfg.ContextSize != 65535 {
		t.Errorf("Expected ContextSize 65535, got %d", cfg.ContextSize)
	}

	// 2. TEXT provider with full params (Cerebras) - no explicit kind specified, defaults to TEXT
	cerebrasDef := "qwen-3.8-27b,https://api.cerebras.ai/v1/chat/completions,csk-test"
	cerebrasParams := "131072,timeout:360,temperature:0.3,minp:0.05,topp:0.95,repeat:1.1,presence:0.3,frequency:0.3"
	cCfg, err := ParseProviderConfig("CEREBRAS", cerebrasDef, "", cerebrasParams)
	if err != nil {
		t.Fatalf("Failed to parse CEREBRAS config: %v", err)
	}
	if cCfg.Kind != ProviderKindText {
		t.Errorf("Expected kind TEXT by default, got %s", cCfg.Kind)
	}
	if cCfg.ContextSize != 131072 {
		t.Errorf("Expected ContextSize 131072, got %d", cCfg.ContextSize)
	}
	if cCfg.Timeout != 360*time.Second {
		t.Errorf("Expected Timeout 360s, got %v", cCfg.Timeout)
	}
	if cCfg.Temperature == nil || *cCfg.Temperature != 0.3 {
		t.Errorf("Expected Temperature 0.3, got %v", cCfg.Temperature)
	}
	if cCfg.MinP == nil || *cCfg.MinP != 0.05 {
		t.Errorf("Expected MinP 0.05, got %v", cCfg.MinP)
	}
	if cCfg.TopP == nil || *cCfg.TopP != 0.95 {
		t.Errorf("Expected TopP 0.95, got %v", cCfg.TopP)
	}
	if cCfg.RepeatPenalty == nil || *cCfg.RepeatPenalty != 1.1 {
		t.Errorf("Expected RepeatPenalty 1.1, got %v", cCfg.RepeatPenalty)
	}
}

func TestProviderRegistryFromEnv(t *testing.T) {
	// Set up environment matching source_setup_gllam.sh (only STRUCT marked explicitly)
	os.Setenv("TYPESAFE", "Jev,https://api.typesafe.ai/v1/systemone,apikey_typesafe")
	os.Setenv("TYPESAFE_KIND", "STRUCT")
	os.Setenv("TYPESAFE_PARAMS", "65535")

	os.Setenv("CEREBRAS", "qwen-3.8-27b,https://api.cerebras.ai/v1/chat/completions,csk-cerebras")
	os.Unsetenv("CEREBRAS_KIND")
	os.Setenv("CEREBRAS_PARAMS", "131072,timeout:360,temperature:0.3,minp:0.05,topp:0.95,repeat:1.1,presence:0.3,frequency:0.3")

	os.Setenv("OPENROUTER", "google/gemini-3.7-flash,https://openrouter.ai/api/v1,sk-openrouter")
	os.Unsetenv("OPENROUTER_KIND")
	os.Setenv("OPENROUTER_PARAMS", "262144,timeout:180,temperature:1.0,minp:0.05,topp:0.95,repeat:1.0,presence:0.3,frequency:0.3,reasoning:medium")

	os.Setenv("LEMOND", "Qwen3.8-27B-UD-Q8_K_XL,http://127.0.0.1:8001,")
	os.Unsetenv("LEMOND_KIND")
	os.Setenv("LEMOND_PARAMS", "262144,timeout:960,temperature:0.3,minp:0.05,topp:0.95,repeat:1.1,presence:0.3,frequency:0.3")

	os.Setenv("SEMANTIC_EXTRACTION", "TYPESAFE")
	os.Setenv("SEARCH_CANDIDATES", "TYPESAFE")
	os.Setenv("QUERY_DECOMPOSITION", "TYPESAFE")
	os.Setenv("ZERO_SHOT_ANSWER", "CEREBRAS")
	os.Setenv("FINAL_ANSWER", "CEREBRAS")
	os.Setenv("FALLBACK_ANSWER", "OPENROUTER")
	os.Setenv("BENCH_RESULT_EVALUATION", "LEMOND")
	os.Setenv("TURN_COMPRESSION", "CEREBRAS")

	reg := NewProviderRegistryFromEnv()

	// Verify task routing
	routing := reg.TaskRouting()
	if routing["SEMANTIC_EXTRACTION"] != "TYPESAFE" {
		t.Errorf("Expected SEMANTIC_EXTRACTION=TYPESAFE, got %s", routing["SEMANTIC_EXTRACTION"])
	}
	if routing["ZERO_SHOT_ANSWER"] != "CEREBRAS" {
		t.Errorf("Expected ZERO_SHOT_ANSWER=CEREBRAS, got %s", routing["ZERO_SHOT_ANSWER"])
	}
	if routing["FALLBACK_ANSWER"] != "OPENROUTER" {
		t.Errorf("Expected FALLBACK_ANSWER=OPENROUTER, got %s", routing["FALLBACK_ANSWER"])
	}
	if routing["BENCH_RESULT_EVALUATION"] != "LEMOND" {
		t.Errorf("Expected BENCH_RESULT_EVALUATION=LEMOND, got %s", routing["BENCH_RESULT_EVALUATION"])
	}
	if routing["TURN_COMPRESSION"] != "CEREBRAS" {
		t.Errorf("Expected TURN_COMPRESSION=CEREBRAS, got %s", routing["TURN_COMPRESSION"])
	}

	// Verify STRUCT client resolution
	structClient, err := reg.GetStructClientForTask("SEARCH_CANDIDATES", "")
	if err != nil {
		t.Fatalf("Failed to get struct client for SEARCH_CANDIDATES: %v", err)
	}
	if structClient.Model != "Jev" {
		t.Errorf("Expected struct client model Jev, got %s", structClient.Model)
	}

	// Verify TEXT client resolution and overrides
	textClient, err := reg.GetTextClientForTask("ZERO_SHOT_ANSWER", "")
	if err != nil {
		t.Fatalf("Failed to get text client for ZERO_SHOT_ANSWER: %v", err)
	}
	if textClient.Model != "qwen-3.8-27b" {
		t.Errorf("Expected text client model qwen-3.8-27b, got %s", textClient.Model)
	}
	if textClient.GetContextSize() != 131072 {
		t.Errorf("Expected context size 131072, got %d", textClient.GetContextSize())
	}
	if textClient.GetTimeout() != 360*time.Second {
		t.Errorf("Expected timeout 360s, got %v", textClient.GetTimeout())
	}
	if textClient.GetTemperature() != 0.3 {
		t.Errorf("Expected temperature 0.3, got %f", textClient.GetTemperature())
	}

	// Verify OpenRouter reasoning override
	openrouterClient, err := reg.GetTextClientForTask("FALLBACK_ANSWER", "")
	if err != nil {
		t.Fatalf("Failed to get text client for FALLBACK_ANSWER: %v", err)
	}
	if openrouterClient.GetReasoningEffort() != "medium" {
		t.Errorf("Expected reasoning effort medium, got %s", openrouterClient.GetReasoningEffort())
	}
}
