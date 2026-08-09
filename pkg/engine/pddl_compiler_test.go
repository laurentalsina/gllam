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

func TestGroundedTemporalAnchorPDDL(t *testing.T) {
	nodes := []memory.SemanticNode{
		{ID: "caddy", Name: "Caddy Service", Type: memory.NodeTypeService},
		{ID: "state-v2-8", Name: "Version 2.8", Type: memory.NodeTypeState},
		{ID: "event-db-migration", Name: "Database Migration", Type: memory.NodeTypeEvent},
	}

	links := []memory.SemanticLink{
		{
			SourceID:         "caddy",
			TargetID:         "state-v2-8",
			Relationship:     "has_state",
			ValidFrom:        "temporal_note",
			TemporalAnchorID: "event-db-migration",
			TemporalRelation: "before",
			TemporalNote:     "before the database migration",
		},
	}

	domain, problem := CompileGraphToPDDL(nodes, links, "(and (has_state caddy state_v2_8))", nil)

	if !strings.Contains(domain, "(happened_before ?a ?b)") {
		t.Errorf("Domain missing happened_before predicate: %s", domain)
	}

	if !strings.Contains(problem, "(happened_before caddy event_db_migration)") {
		t.Errorf("Problem missing grounded happened_before predicate: %s", problem)
	}
}

func TestExtractPDDLGoalRangeIntervals(t *testing.T) {

	nodes := []memory.SemanticNode{
		{ID: "event-db-migration", Name: "database migration", Type: memory.NodeTypeEvent},
		{ID: "event-cache-purge", Name: "cache purge", Type: memory.NodeTypeEvent},
		{ID: "event-release-v2", Name: "production release", Type: memory.NodeTypeEvent},
	}

	// 1. "between X and Y"
	goalBetween := ExtractPDDLGoal("What events occurred between database migration and production release?", nodes, nil)
	if !strings.Contains(goalBetween, "event_db_migration") || !strings.Contains(goalBetween, "event_release_v2") {
		t.Errorf("Between goal failed: %s", goalBetween)
	}

	// 2. "since X"
	goalSince := ExtractPDDLGoal("What has happened since database migration?", nodes, nil)
	if !strings.Contains(goalSince, "(happened_after") || !strings.Contains(goalSince, "event_db_migration") {
		t.Errorf("Since goal failed: %s", goalSince)
	}

	// 3. "until Y"
	goalUntil := ExtractPDDLGoal("What occurred until production release?", nodes, nil)
	if !strings.Contains(goalUntil, "(happened_before") || !strings.Contains(goalUntil, "event_release_v2") {
		t.Errorf("Until goal failed: %s", goalUntil)
	}
}



