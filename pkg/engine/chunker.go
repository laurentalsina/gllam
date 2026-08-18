package engine

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/laurentalsina/gllam/pkg/config"
)


// ValidateTranscriptSemanticCoherence detects adversarial nonsense / high-entropy gibberish text (Trap 9),
// returning false if the text exhibits non-lexical entropy or DoS word-salad characteristics.
func ValidateTranscriptSemanticCoherence(text string) bool {
	cleanText := strings.TrimSpace(text)
	if len(cleanText) < 50 {
		return true // Too short to evaluate
	}

	// 1. Calculate Unigram Shannon Entropy (in bits/char)
	charCounts := make(map[rune]int)
	totalChars := 0
	for _, r := range cleanText {
		if !unicode.IsSpace(r) {
			charCounts[r]++
			totalChars++
		}
	}

	if totalChars == 0 {
		return false
	}

	var entropy float64
	for _, count := range charCounts {
		p := float64(count) / float64(totalChars)
		entropy -= p * (math.Log2(p))
	}

	// Abnormally high unigram character entropy (> 6.2 bits/char) indicates synthetic DoS noise
	if entropy > 6.2 {
		return false
	}

	// 2. Average Word Length & Non-alphanumeric Ratio
	words := strings.Fields(cleanText)
	if len(words) == 0 {
		return false
	}

	totalRuneLen := 0
	nonAlphaRunes := 0
	for _, w := range words {
		for _, r := range w {
			totalRuneLen++
			if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
				nonAlphaRunes++
			}
		}
	}

	avgWordLen := float64(totalRuneLen) / float64(len(words))
	nonAlphaRatio := float64(nonAlphaRunes) / float64(totalRuneLen)

	// Natural prose has avg word length ~3-10 runes and non-alpha ratio < 0.40
	if avgWordLen > 25 || avgWordLen < 1.5 || nonAlphaRatio > 0.40 {
		return false
	}

	return true
}

type SourceTrustInput struct {
	DocumentType string // e.g. "jira_resolved", "pull_request_merged", "confluence_approved", "slack_channel", "email_thread", "draft"
	AuthorID     string // Unique individual identifier e.g. "alice", "bob_contractor", "user-dave"
	AuthorName   string // Human-readable author name e.g. "Alice Smith", "Dave Miller"
	AuthorRole   string // Optional role e.g. "tech_lead", "verified_engineer"
	DocumentText string // Content for evaluating internal coherence
	CreatedAt    int64  // Unix timestamp of document creation
}

// CalculateCompositeTrustWeight evaluates document type heuristics, individual author reliability,
// internal semantic coherence, and temporal freshness to compute a trust weight W in [10, 1000].
func CalculateCompositeTrustWeight(input SourceTrustInput, sysPrompts *config.AgenticMemorySystemPrompts, nowTS int64) int {
	var weight int

	// 1. Document Type Base Heuristic (Dynamic Custom Rules + Built-in Fallbacks)
	matchedType := false
	docTypeKey := strings.ToLower(input.DocumentType)
	if sysPrompts != nil && len(sysPrompts.CustomDocumentTypeRules) > 0 {
		if customRule, ok := sysPrompts.CustomDocumentTypeRules[docTypeKey]; ok && customRule.BaselineTrustWeight > 0 {
			weight = customRule.BaselineTrustWeight
			matchedType = true
		}
	}

	if !matchedType {
		switch docTypeKey {
		case "jira_resolved", "pull_request_merged", "git_commit", "production_config":
			weight = 800
		case "confluence_approved", "architecture_doc", "design_doc", "confluence":
			weight = 700
		case "jira_open", "jira", "slack_channel", "incident_log":
			weight = 600
		case "meeting_notes", "email_thread", "support_ticket":
			weight = 400
		case "confluence_draft", "personal_notes", "draft":
			weight = 200
		default:
			weight = 100
		}

	}


	// 2. Individual Source Reliability Check (Source/Person-specific, NOT just role)
	matchedIndividual := false
	if sysPrompts != nil && len(sysPrompts.SourceReliabilityHeuristics) > 0 {
		authorKeys := []string{strings.ToLower(input.AuthorID), strings.ToLower(input.AuthorName)}
		for _, k := range authorKeys {
			if k != "" {
				if adjustment, ok := sysPrompts.SourceReliabilityHeuristics[k]; ok {
					weight += adjustment
					matchedIndividual = true
					break
				}
			}
		}
	}


	// Fallback to Role Identity Heuristic if no individual match found
	if !matchedIndividual {
		switch strings.ToLower(input.AuthorRole) {
		case "system_ci_cd", "security_bot", "admin":
			weight += 150
		case "tech_lead", "architect", "staff_engineer":
			weight += 100
		case "verified_engineer", "maintainer":
			weight += 50
		}
	}

	// 3. Evaluated Internal Semantic Coherence
	if input.DocumentText != "" {
		if ValidateTranscriptSemanticCoherence(input.DocumentText) {
			weight += 50 // Valid coherent prose boost
		} else {
			weight -= 250 // High entropy / DoS / incoherent penalty
		}
	}

	// 4. Temporal Freshness Adjustment
	if input.CreatedAt > 0 && nowTS > input.CreatedAt {
		ageSec := nowTS - input.CreatedAt
		ageDays := ageSec / 86400

		if ageDays < 30 {
			weight += 50 // Fresh document bonus
		} else if ageDays > 365 {
			weight -= 150 // Very old document penalty (> 1 year)
		} else if ageDays > 180 {
			weight -= 50 // Aged document penalty (> 6 months)
		}
	}

	// Clamp to [10, 1000]
	if weight < 10 {
		weight = 10
	}
	if weight > 1000 {
		weight = 1000
	}

	return weight
}




type Chunk struct {
	Text       string
	StartIndex int
	EndIndex   int
	ChunkIndex int
}

// ChunkTranscript splits a long transcript into overlapping chunks while preserving sentence and turn boundaries.
// chunkSize: target chunk size in characters (e.g. 6000)
// overlapSize: target overlap size in characters (e.g. 2000)
func ChunkTranscript(text string, chunkSize, overlapSize int) []Chunk {
	if len(text) <= chunkSize {
		return []Chunk{{Text: text, StartIndex: 0, EndIndex: len(text), ChunkIndex: 0}}
	}

	var chunks []Chunk
	runes := []rune(text)
	totalRunes := len(runes)

	step := chunkSize - overlapSize
	if step <= 0 {
		step = chunkSize / 2
	}

	chunkIdx := 0
	start := 0

	for start < totalRunes {
		end := start + chunkSize
		if end >= totalRunes {
			end = totalRunes
		} else {
			// Snap end to the nearest sentence or turn boundary backwards to prevent cutting mid-sentence
			snappedEnd := snapToBoundary(runes, end, start+step)
			if snappedEnd > start {
				end = snappedEnd
			}
		}

		chunkText := strings.TrimSpace(string(runes[start:end]))
		if len(chunkText) > 0 {
			chunks = append(chunks, Chunk{
				Text:       chunkText,
				StartIndex: start,
				EndIndex:   end,
				ChunkIndex: chunkIdx,
			})
			chunkIdx++
		}

		if end >= totalRunes {
			break
		}

		// Calculate next start index with overlap
		nextStart := end - overlapSize
		if nextStart <= start {
			nextStart = start + step
		}

		// Snap nextStart to nearest boundary forward/backward so we don't start mid-word/mid-sentence
		snappedStart := snapToBoundaryForward(runes, nextStart, end)
		if snappedStart < end && snappedStart > start {
			nextStart = snappedStart
		}

		start = nextStart
	}

	return chunks
}

// snapToBoundary searches backwards from `pos` down to `minPos` for a turn or sentence boundary
func snapToBoundary(runes []rune, pos, minPos int) int {
	sub := string(runes[minPos:pos])

	// 1. Try splitting at double newline (turn boundary)
	if idx := strings.LastIndex(sub, "\n\n"); idx != -1 {
		runeIdx := utf8.RuneCountInString(sub[:idx])
		if minPos+runeIdx+2 > minPos {
			return minPos + runeIdx + 2
		}
	}

	// 2. Try splitting at single newline
	if idx := strings.LastIndex(sub, "\n"); idx != -1 {
		runeIdx := utf8.RuneCountInString(sub[:idx])
		if minPos+runeIdx+1 > minPos {
			return minPos + runeIdx + 1
		}
	}

	// 3. Try splitting at sentence boundary (. ! ?) followed by space or newline
	for i := pos - 1; i >= minPos; i-- {
		r := runes[i]
		if r == '.' || r == '!' || r == '?' {
			if i+1 < len(runes) && (unicode.IsSpace(runes[i+1]) || runes[i+1] == '\n') {
				return i + 1
			}
		}
	}

	// 4. Fallback to space (word boundary)
	for i := pos - 1; i >= minPos; i-- {
		if unicode.IsSpace(runes[i]) {
			return i + 1
		}
	}

	return pos
}

// snapToBoundaryForward searches forward from `pos` up to `maxPos` for a clean boundary start
func snapToBoundaryForward(runes []rune, pos, maxPos int) int {
	for i := pos; i < maxPos; i++ {
		r := runes[i]
		if r == '\n' || r == '.' || r == '!' || r == '?' {
			if i+1 < maxPos && unicode.IsSpace(runes[i+1]) {
				return i + 1
			}
		}
	}
	// Fallback to next space
	for i := pos; i < maxPos; i++ {
		if unicode.IsSpace(runes[i]) {
			return i + 1
		}
	}
	return pos
}
