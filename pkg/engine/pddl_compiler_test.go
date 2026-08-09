package engine

import (
	"strings"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestCompileGraphToPDDLTyped(t *testing.T) {
	nodes := []memory.SemanticNode{
		{ID: "event-a", Name: "Event A", Type: memory.NodeTypeEvent},
		{ID: "event-b", Name: "Event B", Type: memory.NodeTypeEvent},
		{ID: "state-active", Name: "Active State", Type: memory.NodeTypeState},
	}

	links := []memory.SemanticLink{
		{SourceID: "event-a", TargetID: "event-b", Relationship: "happened_before"},
		{SourceID: "event-a", TargetID: "state-active", Relationship: "has_state"},
	}

	goal := "(and (verified_sequence event_a event_b))"
	domain, problem := CompileGraphToPDDL(nodes, links, goal, nil)

	if !strings.Contains(domain, "(:types event state entity service contradiction - object)") {
		t.Errorf("Domain missing typed declarations: %s", domain)
	}

	if !strings.Contains(problem, "event_a event_b - event") {
		t.Errorf("Problem missing typed event objects: %s", problem)
	}

	if !strings.Contains(problem, "state_active - state") {
		t.Errorf("Problem missing typed state objects: %s", problem)
	}
}

func TestExtractPDDLGoal(t *testing.T) {
	nodes := []memory.SemanticNode{
		{ID: "event-init", Name: "Init Event", Type: memory.NodeTypeEvent},
		{ID: "event-deploy", Name: "Deploy Event", Type: memory.NodeTypeEvent},
	}

	goal := ExtractPDDLGoal("what is the sequence of events before deployment", nodes, nil)
	expected := "(and (verified_sequence event_init event_deploy))"

	if goal != expected {
		t.Errorf("Expected goal %q, got %q", expected, goal)
	}
}

func TestTemporalUncertaintyLink(t *testing.T) {
	link := memory.SemanticLink{
		SourceID:     "pkg-react",
		TargetID:     "state-deprecated",
		Relationship: "has_state",
		ValidFrom:    "temporal_note",
		TemporalNote: "sometime during Q3 2024 before the major release",
	}

	if link.ValidFrom != "temporal_note" {
		t.Errorf("Expected ValidFrom to be 'temporal_note', got %s", link.ValidFrom)
	}
	if link.TemporalNote == "" {
		t.Errorf("Expected non-empty TemporalNote")
	}
}

