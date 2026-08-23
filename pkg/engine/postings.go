package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// CorpusUtterance represents a single utterance (e.g. a dialogue turn) located in the corpus file.
type CorpusUtterance struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	SpeakerID  string `json:"speaker_id"`
	Text       string `json:"text"`
	SourceURI  string `json:"source_uri"`
	LineNumber int    `json:"line_number"`
	StartByte  int64  `json:"start_byte"`
	EndByte    int64  `json:"end_byte"`
}

// Posting represents a term occurrence inside an utterance.
type Posting struct {
	UtteranceID string `json:"utterance_id"`
	Frequency   int    `json:"frequency"`
	Positions   []int  `json:"positions"` // Word offsets (0-indexed) in the utterance
}

// InvertedIndex holds the fast deterministic index.
type InvertedIndex struct {
	Utterances map[string]CorpusUtterance `json:"utterances"`
	Postings   map[string][]Posting       `json:"postings"`
	Sessions   map[string][]string        `json:"sessions"` // Maps session_id -> list of utterance IDs in order
}

// Tokenize splits text into clean lowercase alphanumeric terms.
func Tokenize(text string) []string {
	var words []string
	var current strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			current.WriteRune(unicode.ToLower(r))
		} else {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

// BuildInvertedIndex runs deterministic fine-grained chunking over the corpus file and builds the posting lists.
func BuildInvertedIndex(corpusPath string) (*InvertedIndex, error) {
	file, err := os.Open(corpusPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	absPath, err := filepath.Abs(corpusPath)
	if err != nil {
		absPath = corpusPath
	}
	sourceURI := "file://" + absPath

	index := &InvertedIndex{
		Utterances: make(map[string]CorpusUtterance),
		Postings:   make(map[string][]Posting),
		Sessions:   make(map[string][]string),
	}

	reader := bufio.NewReader(file)
	var lineOffset int64 = 0
	lineNumber := 0

	for {
		lineStr, err := reader.ReadString('\n')
		if err != nil && len(lineStr) == 0 {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		lineNumber++
		lineBytes := []byte(lineStr)
		lineLen := int64(len(lineBytes))

		// Try parsing as structured session JSON (MemArena format)
		var session struct {
			SessionID string `json:"session_id"`
			Turns     []struct {
				TurnID    string `json:"turn_id"`
				SpeakerID string `json:"speaker_id"`
				Text      string `json:"text"`
			} `json:"turns"`
		}

		// Try parsing as BEAM conversation JSON format
		var beamConv struct {
			ConversationID string `json:"conversation_id"`
			Chat           [][]struct {
				ID      int    `json:"id"`
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"chat"`
		}

		errJSON := json.Unmarshal(lineBytes, &session)
		errBeam := json.Unmarshal(lineBytes, &beamConv)

		if errJSON == nil && session.SessionID != "" && len(session.Turns) > 0 {
			lastSpeaker := ""
			for idx, turn := range session.Turns {
				speaker := turn.SpeakerID
				cleanSpeaker := strings.TrimSpace(strings.ToLower(speaker))
				if cleanSpeaker == "say" || strings.HasPrefix(cleanSpeaker, "say:") || strings.HasPrefix(cleanSpeaker, "say ") {
					if lastSpeaker != "" {
						speaker = lastSpeaker
					} else {
						speaker = "unknown"
					}
				} else {
					if speaker != "" {
						lastSpeaker = speaker
					}
				}

				idxInLine := strings.Index(lineStr, turn.Text)
				var startByte, endByte int64
				if idxInLine != -1 {
					startByte = lineOffset + int64(idxInLine)
					endByte = startByte + int64(len(turn.Text))
				} else {
					startByte = lineOffset
					endByte = lineOffset + lineLen
				}

				utteranceID := turn.TurnID
				if utteranceID == "" {
					utteranceID = fmt.Sprintf("%s_t%d", session.SessionID, idx)
				}

				utt := CorpusUtterance{
					ID:         utteranceID,
					SessionID:  session.SessionID,
					SpeakerID:  speaker,
					Text:       turn.Text,
					SourceURI:  sourceURI,
					LineNumber: lineNumber,
					StartByte:  startByte,
					EndByte:    endByte,
				}
				index.Utterances[utteranceID] = utt
				index.Sessions[session.SessionID] = append(index.Sessions[session.SessionID], utteranceID)
				index.addUtteranceToPostings(utt)
			}
		} else if errBeam == nil && beamConv.ConversationID != "" && len(beamConv.Chat) > 0 {
			for sIdx, chatSession := range beamConv.Chat {
				sessionID := fmt.Sprintf("beam-100k-%s-session%d", beamConv.ConversationID, sIdx)
				for mIdx, msg := range chatSession {
					utteranceID := fmt.Sprintf("%s_t%d", sessionID, mIdx)
					speakerID := fmt.Sprintf("%s (id %d)", msg.Role, msg.ID)

					idxInLine := strings.Index(lineStr, msg.Content)
					var startByte, endByte int64
					if idxInLine != -1 {
						startByte = lineOffset + int64(idxInLine)
						endByte = startByte + int64(len(msg.Content))
					} else {
						startByte = lineOffset
						endByte = lineOffset + lineLen
					}

					utt := CorpusUtterance{
						ID:         utteranceID,
						SessionID:  sessionID,
						SpeakerID:  speakerID,
						Text:       msg.Content,
						SourceURI:  sourceURI,
						LineNumber: lineNumber,
						StartByte:  startByte,
						EndByte:    endByte,
					}
					index.Utterances[utteranceID] = utt
					index.Sessions[sessionID] = append(index.Sessions[sessionID], utteranceID)
					index.addUtteranceToPostings(utt)
				}
			}
		} else {
			// Fallback to plain text line
			text := strings.TrimSpace(lineStr)
			if text != "" {
				utteranceID := fmt.Sprintf("line_%d", lineNumber)
				utt := CorpusUtterance{
					ID:         utteranceID,
					SessionID:  fmt.Sprintf("line_%d", lineNumber),
					SpeakerID:  "line",
					Text:       text,
					SourceURI:  sourceURI,
					LineNumber: lineNumber,
					StartByte:  lineOffset,
					EndByte:    lineOffset + lineLen,
				}
				index.Utterances[utteranceID] = utt
				index.Sessions[utt.SessionID] = append(index.Sessions[utt.SessionID], utteranceID)
				index.addUtteranceToPostings(utt)
			}
		}

		lineOffset += lineLen
		if err == io.EOF {
			break
		}
	}

	return index, nil
}

func (index *InvertedIndex) addUtteranceToPostings(utt CorpusUtterance) {
	tokens := Tokenize(utt.Text)

	termPositions := make(map[string][]int)
	for pos, token := range tokens {
		termPositions[token] = append(termPositions[token], pos)
	}

	// Index adjacent bigrams (phrases like "cover letter", "zoom call", etc.)
	for i := 0; i < len(tokens)-1; i++ {
		bigram := tokens[i] + " " + tokens[i+1]
		termPositions[bigram] = append(termPositions[bigram], i)
	}

	for term, positions := range termPositions {
		posting := Posting{
			UtteranceID: utt.ID,
			Frequency:   len(positions),
			Positions:   positions,
		}
		index.Postings[term] = append(index.Postings[term], posting)
	}
}

// PhraseSearch returns IDs of utterances containing the exact phrase in order.
func (index *InvertedIndex) PhraseSearch(phrase string) []string {
	terms := Tokenize(phrase)
	if len(terms) == 0 {
		return nil
	}

	// Look up postings lists for each term
	var lists [][]Posting
	for _, term := range terms {
		list, ok := index.Postings[term]
		if !ok {
			return nil
		}
		lists = append(lists, list)
	}

	// Start with the first term's postings
	candidates := make(map[string][]int)
	for _, p := range lists[0] {
		candidates[p.UtteranceID] = p.Positions
	}

	// Intersect with subsequent terms
	for i := 1; i < len(terms); i++ {
		nextList := lists[i]
		nextMap := make(map[string][]int)
		for _, p := range nextList {
			nextMap[p.UtteranceID] = p.Positions
		}

		newCandidates := make(map[string][]int)
		for uttID, posList1 := range candidates {
			posList2, ok := nextMap[uttID]
			if !ok {
				continue
			}

			var validStarts []int
			for _, p1 := range posList1 {
				for _, p2 := range posList2 {
					if p2 == p1+i {
						validStarts = append(validStarts, p1)
						break
					}
				}
			}
			if len(validStarts) > 0 {
				newCandidates[uttID] = validStarts
			}
		}
		candidates = newCandidates
	}

	var results []string
	for uttID := range candidates {
		results = append(results, uttID)
	}
	return results
}

// ProximitySearch finds utterances where termA and termB appear within maxDistance words of each other.
func (index *InvertedIndex) ProximitySearch(termA, termB string, maxDistance int) []string {
	termA = strings.ToLower(termA)
	termB = strings.ToLower(termB)

	listA, okA := index.Postings[termA]
	listB, okB := index.Postings[termB]
	if !okA || !okB {
		return nil
	}

	mapB := make(map[string][]int)
	for _, p := range listB {
		mapB[p.UtteranceID] = p.Positions
	}

	var results []string
	for _, pA := range listA {
		posB, ok := mapB[pA.UtteranceID]
		if !ok {
			continue
		}

		match := false
		for _, posAVal := range pA.Positions {
			for _, posBVal := range posB {
				diff := posAVal - posBVal
				if diff < 0 {
					diff = -diff
				}
				if diff <= maxDistance {
					match = true
					break
				}
			}
			if match {
				break
			}
		}

		if match {
			results = append(results, pA.UtteranceID)
		}
	}

	return results
}
