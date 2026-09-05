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
