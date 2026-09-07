package engine

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/laurentalsina/gllam/pkg/config"
	_ "github.com/mattn/go-sqlite3"
)

// CompressionConfig specifies parameters for dialogue turn compression
type CompressionConfig struct {
	TargetCompressionPercent int           // Target word count as percent of original (e.g. 40)
	ReductionPercent         int           // Target reduction percent (e.g. 60)
	MinWords                 int           // Minimum word count to trigger compression
	Concurrency              int           // Number of concurrent compression workers
	PromptTemplate           string        // Compression prompt template
	Timeout                  time.Duration // Timeout for LLM generation
	Force                    bool          // Force compression even if cached in DB
}

// DefaultCompressionConfig returns baseline compression settings
func DefaultCompressionConfig() CompressionConfig {
	return CompressionConfig{
		TargetCompressionPercent: 40,
		ReductionPercent:         60,
		MinWords:                 15,
		Concurrency:              4,
		PromptTemplate:           config.DefaultPreprocessCompressionPrompt,
		Timeout:                  120 * time.Second,
		Force:                    false,
	}
}

// CompressionStats tracks token/word savings and cache efficiency
type CompressionStats struct {
	TotalTurns      int
	ProcessedTurns  int
	CachedTurns     int
	SkippedTurns    int
	ErrorTurns      int
	OriginalWords   int64
	CompressedWords int64
	Duration        time.Duration
}

// ReductionPercentage returns the aggregate reduction percentage achieved
func (cs CompressionStats) ReductionPercentage() float64 {
	if cs.OriginalWords == 0 {
		return 0.0
	}
	return float64(cs.OriginalWords-cs.CompressedWords) / float64(cs.OriginalWords) * 100.0
}

// CompressionPercentage returns the aggregate compressed word percentage
func (cs CompressionStats) CompressionPercentage() float64 {
	if cs.OriginalWords == 0 {
		return 100.0
	}
	return float64(cs.CompressedWords) / float64(cs.OriginalWords) * 100.0
}

// MessageCompressor coordinates LLM turn compression with SQLite persistence
type MessageCompressor struct {
	llmClient *LLMClient
	db        *sql.DB
	cfg       CompressionConfig
	dbMu      sync.RWMutex
	statsMu   sync.Mutex
	stats     CompressionStats
}

// NewMessageCompressor creates a new compressor instance
func NewMessageCompressor(llmClient *LLMClient, dbPath string, cfg CompressionConfig) (*MessageCompressor, error) {
	if cfg.TargetCompressionPercent <= 0 {
		cfg.TargetCompressionPercent = 40
	}
	if cfg.ReductionPercent <= 0 {
		cfg.ReductionPercent = 100 - cfg.TargetCompressionPercent
	}
	if cfg.MinWords <= 0 {
		cfg.MinWords = 15
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	if cfg.PromptTemplate == "" {
		cfg.PromptTemplate = config.DefaultPreprocessCompressionPrompt
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}

	var db *sql.DB
	if dbPath != "" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil && filepath.Dir(dbPath) != "." {
			return nil, fmt.Errorf("failed to create cache directory: %w", err)
		}
		var err error
		db, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
		if err != nil {
			return nil, fmt.Errorf("failed to open compression cache db: %w", err)
		}

		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS compressed_messages (
			message_hash TEXT PRIMARY KEY,
			role TEXT NOT NULL,
			original_text TEXT NOT NULL,
			compressed_text TEXT NOT NULL,
			original_words INTEGER NOT NULL,
			compressed_words INTEGER NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_compressed_created ON compressed_messages(created_at);`)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to initialize compression cache schema: %w", err)
		}
	}

	return &MessageCompressor{
		llmClient: llmClient,
		db:        db,
		cfg:       cfg,
	}, nil
}

// Close closes the underlying SQLite database
func (mc *MessageCompressor) Close() error {
	if mc.db != nil {
		return mc.db.Close()
	}
	return nil
}

// Stats returns a copy of current cumulative statistics
func (mc *MessageCompressor) Stats() CompressionStats {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	return mc.stats
}

// BuildCompressionSystemPrompt formats the prompt template with exact percentages
func BuildCompressionSystemPrompt(template string, targetPct, reductionPct int) string {
	if template == "" {
		template = config.DefaultPreprocessCompressionPrompt
	}
	s := strings.ReplaceAll(template, "{{TARGET_COMPRESSION_PERCENT}}", fmt.Sprintf("%d", targetPct))
	s = strings.ReplaceAll(s, "{{REDUCTION_PERCENT}}", fmt.Sprintf("%d", reductionPct))
	return s
}

var thinkTagRegex = regexp.MustCompile(`(?s)<think>.*?</think>`)

// CleanCompressedText strips extraneous reasoning, code block formatting, and echoed message tags
func CleanCompressedText(s string) string {
	s = strings.TrimSpace(s)
	// Strip thinking tags if present
	s = thinkTagRegex.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)

	// Strip outer markdown code blocks if the model wrapped the response in ```
	if strings.HasPrefix(s, "```") && strings.HasSuffix(s, "```") {
		lines := strings.Split(s, "\n")
		if len(lines) >= 2 {
			s = strings.Join(lines[1:len(lines)-1], "\n")
			s = strings.TrimSpace(s)
		}
	}

	// Strip enclosing <user_message> or <assistant_message> tags if echoed
	s = strings.TrimPrefix(s, "<user_message>")
	s = strings.TrimPrefix(s, "<assistant_message>")
	s = strings.TrimSuffix(s, "</user_message>")
	s = strings.TrimSuffix(s, "</assistant_message>")

	return strings.TrimSpace(s)
}

// CompressMessage compresses a single message turn according to rules, consulting the cache first
func (mc *MessageCompressor) CompressMessage(ctx context.Context, role, text string) (string, error) {
	origWords := len(strings.Fields(text))

	// If shorter than minimum threshold, preserve as-is without LLM call
	if origWords < mc.cfg.MinWords {
		mc.statsMu.Lock()
		mc.stats.TotalTurns++
		mc.stats.SkippedTurns++
		mc.stats.OriginalWords += int64(origWords)
		mc.stats.CompressedWords += int64(origWords)
		mc.statsMu.Unlock()
		return text, nil
	}

	// Compute message hash for cache lookup
	hasher := sha256.New()
	hasher.Write([]byte(fmt.Sprintf("%s:%d:%s", strings.ToLower(role), mc.cfg.TargetCompressionPercent, text)))
	msgHash := hex.EncodeToString(hasher.Sum(nil))

	// Check SQLite cache
	if !mc.cfg.Force && mc.db != nil {
		mc.dbMu.RLock()
		var cachedText string
		var cachedWords int
		err := mc.db.QueryRowContext(ctx, "SELECT compressed_text, compressed_words FROM compressed_messages WHERE message_hash = ?", msgHash).Scan(&cachedText, &cachedWords)
		mc.dbMu.RUnlock()

		if err == nil && cachedText != "" {
			mc.statsMu.Lock()
			mc.stats.TotalTurns++
			mc.stats.CachedTurns++
			mc.stats.OriginalWords += int64(origWords)
			mc.stats.CompressedWords += int64(cachedWords)
			mc.statsMu.Unlock()
			return cachedText, nil
		}
	}

	// No LLM client provided - return original
	if mc.llmClient == nil {
		mc.statsMu.Lock()
		mc.stats.TotalTurns++
		mc.stats.SkippedTurns++
		mc.stats.OriginalWords += int64(origWords)
		mc.stats.CompressedWords += int64(origWords)
		mc.statsMu.Unlock()
		return text, nil
	}

	// Build user prompt with appropriate role tag
	var userPrompt string
	cleanRole := strings.ToLower(strings.TrimSpace(role))
	if cleanRole == "user" || strings.HasPrefix(cleanRole, "user") {
		userPrompt = fmt.Sprintf("<user_message>\n%s\n</user_message>", text)
	} else {
		userPrompt = fmt.Sprintf("<assistant_message>\n%s\n</assistant_message>", text)
	}

	systemPrompt := BuildCompressionSystemPrompt(mc.cfg.PromptTemplate, mc.cfg.TargetCompressionPercent, mc.cfg.ReductionPercent)

	// Invoke LLM
	callCtx, cancel := context.WithTimeout(ctx, mc.cfg.Timeout)
	defer cancel()

	resp, err := mc.llmClient.Generate(callCtx, systemPrompt, userPrompt)
	if err != nil {
		// On LLM failure, log and fall back safely to original text
		mc.statsMu.Lock()
		mc.stats.TotalTurns++
		mc.stats.ErrorTurns++
		mc.stats.OriginalWords += int64(origWords)
		mc.stats.CompressedWords += int64(origWords)
		mc.statsMu.Unlock()
		return text, fmt.Errorf("compression request failed: %w", err)
	}

	cleaned := CleanCompressedText(resp)
	if cleaned == "" {
		// Output empty, fall back to original
		mc.statsMu.Lock()
		mc.stats.TotalTurns++
		mc.stats.ErrorTurns++
		mc.stats.OriginalWords += int64(origWords)
		mc.stats.CompressedWords += int64(origWords)
		mc.statsMu.Unlock()
		return text, nil
	}

	compWords := len(strings.Fields(cleaned))

	// Store in cache
	if mc.db != nil {
		mc.dbMu.Lock()
		now := time.Now().UTC().Format(time.RFC3339)
		_, _ = mc.db.ExecContext(ctx, `INSERT OR REPLACE INTO compressed_messages 
			(message_hash, role, original_text, compressed_text, original_words, compressed_words, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			msgHash, role, text, cleaned, origWords, compWords, now)
		mc.dbMu.Unlock()
	}

	mc.statsMu.Lock()
	mc.stats.TotalTurns++
	mc.stats.ProcessedTurns++
	mc.stats.OriginalWords += int64(origWords)
	mc.stats.CompressedWords += int64(compWords)
	mc.statsMu.Unlock()

	return cleaned, nil
}

type turnTarget struct {
	sessionIdx int
	turnIdx    int
	role       string
	content    string
}

// PreprocessBeamConversation processes all turns in a BEAM conversation JSON object
func (mc *MessageCompressor) PreprocessBeamConversation(ctx context.Context, convMap map[string]interface{}) error {
	rawChat, ok := convMap["chat"]
	if !ok {
		return nil
	}

	chatList, ok := rawChat.([]interface{})
	if !ok {
		return nil
	}

	var targets []turnTarget
	for sIdx, rawSess := range chatList {
		sessList, ok := rawSess.([]interface{})
		if !ok {
			continue
		}
		for tIdx, rawTurn := range sessList {
			turnMap, ok := rawTurn.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := turnMap["role"].(string)
			content, _ := turnMap["content"].(string)
			if content != "" {
				targets = append(targets, turnTarget{
					sessionIdx: sIdx,
					turnIdx:    tIdx,
					role:       role,
					content:    content,
				})
			}
		}
	}

	if len(targets) == 0 {
		return nil
	}

	concurrency := mc.cfg.Concurrency
	if concurrency > len(targets) {
		concurrency = len(targets)
	}

	results := make([]string, len(targets))
	var wg sync.WaitGroup
	targetChan := make(chan int, len(targets))

	for i := 0; i < len(targets); i++ {
		targetChan <- i
	}
	close(targetChan)

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range targetChan {
				select {
				case <-ctx.Done():
					results[idx] = targets[idx].content
					return
				default:
				}
				comp, err := mc.CompressMessage(ctx, targets[idx].role, targets[idx].content)
				if err != nil {
					results[idx] = targets[idx].content
				} else {
					results[idx] = comp
				}
			}
		}()
	}

	wg.Wait()

	// Apply compressed text back into conversation map
	for i, t := range targets {
		sessList := chatList[t.sessionIdx].([]interface{})
		turnMap := sessList[t.turnIdx].(map[string]interface{})
		turnMap["content"] = results[i]
	}

	return nil
}

// PreprocessBeamCorpus reads an input JSONL corpus, applies compression, and writes the output JSONL
func (mc *MessageCompressor) PreprocessBeamCorpus(
	ctx context.Context,
	inputPath, outputPath string,
	targetConvIDs map[string]bool,
	progressCallback func(convID string, isTarget bool, convStats CompressionStats),
) error {
	inFile, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("failed to open input corpus: %w", err)
	}
	defer inFile.Close()

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil && filepath.Dir(outputPath) != "." {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	tmpOutPath := outputPath + ".tmp"
	outFile, err := os.Create(tmpOutPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}

	writer := bufio.NewWriter(outFile)
	scanner := bufio.NewScanner(inFile)
	// Expand buffer for large BEAM conversation lines (~5-10MB per line)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 64*1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		lineBytes := scanner.Bytes()
		if len(lineBytes) == 0 {
			continue
		}

		var conv map[string]interface{}
		if err := json.Unmarshal(lineBytes, &conv); err != nil {
			// If not valid JSON conversation, write line as-is
			writer.Write(lineBytes)
			writer.WriteString("\n")
			continue
		}

		convID := fmt.Sprintf("%v", conv["conversation_id"])
		cleanConvID := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(convID), "BEAM_100K_Conversation_"), "Conversation_")

		isTarget := len(targetConvIDs) == 0 || targetConvIDs[convID] || targetConvIDs[cleanConvID]

		t0 := time.Now()
		startStats := mc.Stats()

		if isTarget {
			if err := mc.PreprocessBeamConversation(ctx, conv); err != nil {
				return fmt.Errorf("failed to preprocess conversation %s: %w", convID, err)
			}
		}

		endStats := mc.Stats()
		deltaStats := CompressionStats{
			TotalTurns:      endStats.TotalTurns - startStats.TotalTurns,
			ProcessedTurns:  endStats.ProcessedTurns - startStats.ProcessedTurns,
			CachedTurns:     endStats.CachedTurns - startStats.CachedTurns,
			SkippedTurns:    endStats.SkippedTurns - startStats.SkippedTurns,
			ErrorTurns:      endStats.ErrorTurns - startStats.ErrorTurns,
			OriginalWords:   endStats.OriginalWords - startStats.OriginalWords,
			CompressedWords: endStats.CompressedWords - startStats.CompressedWords,
			Duration:        time.Since(t0),
		}

		if progressCallback != nil {
			progressCallback(convID, isTarget, deltaStats)
		}

		outBytes, err := json.Marshal(conv)
		if err != nil {
			writer.Write(lineBytes)
		} else {
			writer.Write(outBytes)
		}
		writer.WriteString("\n")
		writer.Flush()
	}

	if err := scanner.Err(); err != nil {
		outFile.Close()
		os.Remove(tmpOutPath)
		return fmt.Errorf("scanner error while reading corpus: %w", err)
	}

	if err := writer.Flush(); err != nil {
		outFile.Close()
		os.Remove(tmpOutPath)
		return fmt.Errorf("failed to flush output corpus: %w", err)
	}
	outFile.Close()

	if err := os.Rename(tmpOutPath, outputPath); err != nil {
		return fmt.Errorf("failed to move temp file to %s: %w", outputPath, err)
	}

	return nil
}
