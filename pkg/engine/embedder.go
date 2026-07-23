package engine

import (
    "bytes"
    "context"
    "encoding/binary"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "time"
)

// Embedder is the interface for generating embedding vectors from text.
type Embedder interface {
    // Embed generates an embedding vector for the given text.
    // Returns an error if the embedding service is unavailable.
    Embed(ctx context.Context, text string) ([]float32, error)
}

// LlamaEmbedder generates embeddings via a llama.cpp server instance.
type LlamaEmbedder struct {
    BaseURL string
    client  *http.Client
}

// NewLlamaEmbedder creates a new LlamaEmbedder pointing to the specified server.
func NewLlamaEmbedder(baseURL string) *LlamaEmbedder {
    return &LlamaEmbedder{
        BaseURL: baseURL,
        client: &http.Client{
            Timeout: 30 * time.Second,
        },
    }
}

// serializeEmbedding converts a float32 slice to a byte slice for storage in vec0.
func serializeEmbedding(vec []float32) ([]byte, error) {
    buf := new(bytes.Buffer)
    if err := binary.Write(buf, binary.LittleEndian, vec); err != nil {
        return nil, err
    }
    return buf.Bytes(), nil
}

// Embed sends text to the llama.cpp server and returns the embedding vector.
func (l *LlamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
    reqBody := map[string]string{
        "content": text,
    }
    body, err := json.Marshal(reqBody)
    if err != nil {
        return nil, fmt.Errorf("failed to marshal embed request: %w", err)
    }

    req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.BaseURL+"/embedding", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("failed to create embed request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")

    resp, err := l.client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("llama.cpp server unreachable at %s: %w", l.BaseURL, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("llama.cpp server returned %d: %s", resp.StatusCode, string(respBody))
    }

    var respData []struct {
        Embedding [][]float32 `json:"embedding"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&respData); err != nil {
        return nil, fmt.Errorf("failed to decode embedding response: %w", err)
    }

    if len(respData) == 0 || len(respData[0].Embedding) == 0 || len(respData[0].Embedding[0]) == 0 {
        return nil, fmt.Errorf("empty embedding returned from server")
    }

    return respData[0].Embedding[0], nil
}
