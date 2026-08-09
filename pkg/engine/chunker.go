package engine

import (
	"strings"
	"unicode"
)

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
	if idx := strings.LastIndex(sub, "\n\n"); idx != -1 && minPos+idx+2 > minPos {
		return minPos + idx + 2
	}

	// 2. Try splitting at single newline
	if idx := strings.LastIndex(sub, "\n"); idx != -1 && minPos+idx+1 > minPos {
		return minPos + idx + 1
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
