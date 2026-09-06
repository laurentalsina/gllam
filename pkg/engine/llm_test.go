package engine

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestLLMClientTimeoutAndContextEnv(t *testing.T) {
	os.Setenv("STRONG_MODEL_CONTEXT", "131072")
	os.Setenv("FAST_MODEL_CONTEXT", "65536")
	os.Setenv("STRONG_MODEL_TIMEOUT", "180")
	os.Setenv("FAST_MODEL_TIMEOUT", "120")

	strongClient := NewLLMClient("http://127.0.0.1:8888")
	strongClient.Tier = "strong"

	fastClient := NewLLMClient("http://127.0.0.1:8888")
	fastClient.Tier = "fast"

	if strongClient.GetContextSize() != 131072 {
		t.Errorf("Expected strong context 131072, got %d", strongClient.GetContextSize())
	}
	if fastClient.GetContextSize() != 65536 {
		t.Errorf("Expected fast context 65536, got %d", fastClient.GetContextSize())
	}

	if strongClient.GetTimeout() != 180*time.Second {
		t.Errorf("Expected strong timeout 180s, got %v", strongClient.GetTimeout())
	}
	if fastClient.GetTimeout() != 120*time.Second {
		t.Errorf("Expected fast timeout 120s, got %v", fastClient.GetTimeout())
	}
}

func TestRepairTruncatedJSON(t *testing.T) {
	// Case 1: Truncated inside links array
	truncated1 := `{"nodes":[{"id":"n1","name":"A","type":"entity"}],"links":[{"source_id":"n1","target_id":"n2","relationship":"rel"`
	repaired1 := RepairTruncatedJSON(truncated1)
	var obj1 map[string]interface{}
	if err := json.Unmarshal([]byte(repaired1), &obj1); err != nil {
		t.Fatalf("Repaired JSON 1 failed to unmarshal: %v (content: %s)", err, repaired1)
	}

	// Case 2: Truncated after full link object but unclosed array and outer object
	truncated2 := `{"nodes":[{"id":"n1","name":"A","type":"entity"}],"links":[{"source_id":"n1","target_id":"n2","relationship":"rel"}`
	repaired2 := RepairTruncatedJSON(truncated2)
	var obj2 map[string]interface{}
	if err := json.Unmarshal([]byte(repaired2), &obj2); err != nil {
		t.Fatalf("Repaired JSON 2 failed to unmarshal: %v (content: %s)", err, repaired2)
	}

	// Case 3: Already complete JSON
	complete := `{"nodes":[{"id":"n1","name":"A","type":"entity"}],"links":[]}`
	if RepairTruncatedJSON(complete) != complete {
		t.Errorf("Expected complete JSON to remain unchanged")
	}
}

func TestLLMClientSamplingParameters(t *testing.T) {
	os.Setenv("FAST_MODEL_TEMPERATURE", "0.3")
	os.Setenv("FAST_MODEL_MINP", "0.05")
	os.Setenv("FAST_MODEL_TOPP", "0.95")
	os.Setenv("FAST_MODEL_REPEATPENALTY", "1.1")
	os.Setenv("FAST_MODEL_PRESENCEPENALTY", "0.3")
	os.Setenv("FAST_MODEL_FREQUENCYPENALTY", "0.3")

	os.Setenv("STRONG_MODEL_TEMPERATURE", "0.2")
	os.Setenv("STRONG_MODEL_MINP", "0.08")
	os.Setenv("STRONG_MODEL_TOPP", "0.90")
	os.Setenv("STRONG_MODEL_REPEATPENALTY", "1.15")
	os.Setenv("STRONG_MODEL_PRESENCEPENALTY", "0.25")
	os.Setenv("STRONG_MODEL_FREQUENCYPENALTY", "0.25")

	fastClient := NewLLMClient("http://127.0.0.1:8888")
	fastClient.Tier = "fast"

	strongClient := NewLLMClient("http://127.0.0.1:8888")
	strongClient.Tier = "strong"

	if fastClient.GetTemperature() != 0.3 {
		t.Errorf("Expected fast temperature 0.3, got %f", fastClient.GetTemperature())
	}
	if *fastClient.GetMinP() != 0.05 {
		t.Errorf("Expected fast min_p 0.05, got %f", *fastClient.GetMinP())
	}
	if *fastClient.GetTopP() != 0.95 {
		t.Errorf("Expected fast top_p 0.95, got %f", *fastClient.GetTopP())
	}
	if *fastClient.GetRepeatPenalty() != 1.1 {
		t.Errorf("Expected fast repeat_penalty 1.1, got %f", *fastClient.GetRepeatPenalty())
	}
	if *fastClient.GetPresencePenalty() != 0.3 {
		t.Errorf("Expected fast presence_penalty 0.3, got %f", *fastClient.GetPresencePenalty())
	}
	if *fastClient.GetFrequencyPenalty() != 0.3 {
		t.Errorf("Expected fast frequency_penalty 0.3, got %f", *fastClient.GetFrequencyPenalty())
	}

	if strongClient.GetTemperature() != 0.2 {
		t.Errorf("Expected strong temperature 0.2, got %f", strongClient.GetTemperature())
	}
	if *strongClient.GetMinP() != 0.08 {
		t.Errorf("Expected strong min_p 0.08, got %f", *strongClient.GetMinP())
	}

	// Verify JSON serialization includes all sampling parameters
	req := ChatCompletionRequest{
		Model:             "qwen-2.5-7b",
		Temperature:       fastClient.GetTemperature(),
		TopP:              fastClient.GetTopP(),
		MinP:              fastClient.GetMinP(),
		RepeatPenalty:     fastClient.GetRepeatPenalty(),
		RepetitionPenalty: fastClient.GetRepeatPenalty(),
		PresencePenalty:   fastClient.GetPresencePenalty(),
		FrequencyPenalty:  fastClient.GetFrequencyPenalty(),
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal ChatCompletionRequest: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal serialized request: %v", err)
	}

	for _, field := range []string{"temperature", "top_p", "min_p", "repeat_penalty", "repetition_penalty", "presence_penalty", "frequency_penalty"} {
		if _, ok := parsed[field]; !ok {
			t.Errorf("Expected JSON to contain key %q, got: %s", field, string(data))
		}
	}
}
