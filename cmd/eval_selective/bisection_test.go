package main

import (
	"strings"
	"testing"
)

func TestSplitTranscriptInHalf(t *testing.T) {
	transcript := `user (id 1): Hello, I need some help planning my project timeline.
assistant (id 2): Sure, I would be happy to help you plan out the timeline!
user (id 3): First, I want to draft the document by March 25.
assistant (id 4): Drafting by March 25 is a reasonable deadline.
user (id 5): Then revise it by April 5.
assistant (id 6): That gives you ample time for revision.`

	part1, part2 := splitTranscriptInHalf(transcript)

	if part1 == "" || part2 == "" {
		t.Fatalf("Expected non-empty parts, got part1=%q, part2=%q", part1, part2)
	}

	if !strings.HasPrefix(part1, "user (id 1):") {
		t.Errorf("part1 does not start with first speaker: %q", part1)
	}

	trimmedPart2 := strings.TrimSpace(part2)
	if !strings.HasPrefix(trimmedPart2, "user") && !strings.HasPrefix(trimmedPart2, "assistant") {
		t.Errorf("part2 did not split at a speaker turn boundary: %q", trimmedPart2)
	}
}
