package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTypeSafeClientEvaluate(t *testing.T) {
	// Mock TypeSafe System One server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("Expected path /v1/systemone, got %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test_key" {
			t.Errorf("Expected Authorization Bearer test_key, got %s", auth)
		}

		var req SystemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Failed to decode request: %v", err)
		}

		answers := make(map[string]SystemOneAnswer)
		for qKey, qVal := range req.Questions {
			switch qVal.Type {
			case "noul":
				n := 0.88
				answers[qKey] = SystemOneAnswer{Type: "noul", Noul: &n}
			case "choice":
				c := "technical"
				conf := 0.92
				answers[qKey] = SystemOneAnswer{
					Type:       "choice",
					Choice:     &c,
					Confidence: &conf,
					Probabilities: map[string]float64{
						"technical": 0.85,
						"billing":   0.15,
					},
				}
			case "score":
				s := 2.5
				conf := 0.90
				answers[qKey] = SystemOneAnswer{
					Type:       "score",
					Score:      &s,
					Confidence: &conf,
				}
			}
		}

		resp := SystemOneResponse{
			Model:   req.Model,
			Answers: answers,
			Usage: SystemOneUsage{
				InputTokens:  150,
				OutputTokens: 25,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewTypeSafeClient(server.URL, "test_key", "jev-latest", 10*time.Second)

	// 1. Test Evaluate with multiple questions
	questions := map[string]SystemOneQuestion{
		"is_relevant": {
			Type:         "noul",
			Instructions: "Is this relevant?",
		},
		"category": {
			Type:         "choice",
			Instructions: "Which category?",
			Criteria: map[string]string{
				"technical": "Tech issues",
				"billing":   "Billing issues",
			},
		},
		"urgency": {
			Type:         "score",
			Instructions: "Rate urgency",
			Criteria:     []string{"Low", "Medium", "High"},
		},
	}

	ctx := context.Background()
	resp, err := client.Evaluate(ctx, "Test state content", questions)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}

	if resp.Model != "jev-latest" {
		t.Errorf("Expected model jev-latest, got %s", resp.Model)
	}
	if ans, ok := resp.Answers["is_relevant"]; !ok || ans.Noul == nil || *ans.Noul != 0.88 {
		t.Errorf("Expected is_relevant noul 0.88, got %v", ans.Noul)
	}
	if ans, ok := resp.Answers["category"]; !ok || ans.Choice == nil || *ans.Choice != "technical" {
		t.Errorf("Expected category choice technical, got %v", ans.Choice)
	}

	// 2. Test EvaluateNoul helper
	noul, err := client.EvaluateNoul(ctx, "Test state", "Is this relevant?", nil)
	if err != nil {
		t.Fatalf("EvaluateNoul failed: %v", err)
	}
	// Note: in mock, EvaluateNoul asks question "q", but server returns fixed keys.
	// Let's verify error handling when question key is missing.
	_ = noul
}
