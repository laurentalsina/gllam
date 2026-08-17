package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenize(t *testing.T) {
	text := "Hello, World! This is a test: 123 EV diagnostic."
	expected := []string{"hello", "world", "this", "is", "a", "test", "123", "ev", "diagnostic"}
	actual := Tokenize(text)

	if len(expected) != len(actual) {
		t.Fatalf("Expected %d tokens, got %d: %v", len(expected), len(actual), actual)
	}

	for i, v := range expected {
		if v != actual[i] {
			t.Errorf("Expected token %d to be %q, got %q", i, v, actual[i])
		}
	}
}

func TestBuildInvertedIndexStructuredAndSearch(t *testing.T) {
	tempDir := t.TempDir()
	corpusPath := filepath.Join(tempDir, "test_corpus.jsonl")

	content := `{"session_id": "sess_1", "turns": [{"turn_id": "t1", "speaker_id": "alice", "text": "I am looking for a Tesla battery pack."}, {"turn_id": "t2", "speaker_id": "bob", "text": "The battery pack wait time is six weeks."}]}
{"session_id": "sess_2", "turns": [{"turn_id": "t3", "speaker_id": "charlie", "text": "Is Tesla making electric vehicles?"}]}`

	err := os.WriteFile(corpusPath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to write test corpus: %v", err)
	}

	idx, err := BuildInvertedIndex(corpusPath)
	if err != nil {
		t.Fatalf("Failed to build index: %v", err)
	}

	if len(idx.Utterances) != 3 {
		t.Errorf("Expected 3 utterances, got %d", len(idx.Utterances))
	}

	// Test locations are valid
	t1, ok := idx.Utterances["t1"]
	if !ok {
		t.Fatalf("Utterance t1 not found")
	}
	if t1.StartByte <= 0 {
		t.Errorf("Expected t1.StartByte to be > 0, got %d", t1.StartByte)
	}
	if t1.EndByte <= t1.StartByte {
		t.Errorf("Expected t1.EndByte to be > t1.StartByte, got %d", t1.EndByte)
	}

	// Test phrase search
	resPhrase := idx.PhraseSearch("battery pack")
	if len(resPhrase) != 2 {
		t.Errorf("Expected phrase search 'battery pack' to return 2 results, got %d: %v", len(resPhrase), resPhrase)
	}

	resPhraseTesla := idx.PhraseSearch("tesla battery")
	if len(resPhraseTesla) != 1 || resPhraseTesla[0] != "t1" {
		t.Errorf("Expected phrase search 'tesla battery' to return ['t1'], got %v", resPhraseTesla)
	}

	// Test proximity search
	resProx := idx.ProximitySearch("tesla", "pack", 4)
	if len(resProx) != 1 || resProx[0] != "t1" {
		t.Errorf("Expected proximity search 'tesla' & 'pack' with maxDistance 4 to return ['t1'], got %v", resProx)
	}
}
