package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestBiTemporalPointInTimeRetrieval(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_bitemporal.db")

	embedder := &MockAsyncEmbedder{}
	gllam, err := NewGllamEngine(dbPath, embedder)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// 1. Create nodes
	dbNode := memory.SemanticNode{ID: "node-db", Name: "Primary Database", Type: memory.NodeTypeEntity}
	pg13Node := memory.SemanticNode{ID: "node-pg13", Name: "PostgreSQL 13", Type: memory.NodeTypeEntity}
	pg15Node := memory.SemanticNode{ID: "node-pg15", Name: "PostgreSQL 15", Type: memory.NodeTypeEntity}
	_ = gllam.UpsertNode(ctx, dbNode)
	_ = gllam.UpsertNode(ctx, pg13Node)
	_ = gllam.UpsertNode(ctx, pg15Node)

	// Timestamps:
	// 2021-01-01 00:00:00 UTC = 1609459200
	// 2025-01-01 00:00:00 UTC = 1735689600
	ts2021 := int64(1609459200)
	ts2025 := int64(1735689600)


	// Fact 1: Postgres 13 active from 2020 to 2023 (valid_from = 2020, valid_until = 2023)
	strUntil2023 := "1672531200"
	linkPg13 := memory.SemanticLink{
		SourceID:     "node-db",
		TargetID:     "node-pg13",
		Relationship: "uses_version",
		ValidFrom:    "1577836800", // 2020-01-01
		ValidUntil:   &strUntil2023,
	}
	if err := gllam.AddEdge(ctx, linkPg13); err != nil {
		t.Fatalf("Failed to add linkPg13: %v", err)
	}

	// Fact 2: Postgres 15 active from 2023 onwards (valid_from = 2023, valid_until = nil)
	linkPg15 := memory.SemanticLink{
		SourceID:     "node-db",
		TargetID:     "node-pg15",
		Relationship: "uses_version",
		ValidFrom:    "1672531200", // 2023-01-01
		ValidUntil:   nil,
	}
	if err := gllam.AddEdge(ctx, linkPg15); err != nil {
		t.Fatalf("Failed to add linkPg15: %v", err)
	}

	// 2. Perform Time Travel RAG Query as of 2021 (ts2021)
	nodes2021, err := gllam.RetrieveHybridNeedleWithTime(ctx, "Primary Database version", []string{"node-db"}, "test-source", 10, &ts2021)
	if err != nil {
		t.Fatalf("Time travel query 2021 failed: %v", err)
	}

	// Assert that in 2021, node-db links to node-pg13, NOT node-pg15
	var foundPg13_2021, foundPg15_2021 bool
	for _, sn := range nodes2021 {
		if sn.Node.ID == "node-db" {
			for _, l := range sn.Links {
				if l.TargetID == "node-pg13" {
					foundPg13_2021 = true
				}
				if l.TargetID == "node-pg15" {
					foundPg15_2021 = true
				}
			}
		}
	}
	if !foundPg13_2021 || foundPg15_2021 {
		t.Fatalf("Expected 2021 query to return active PostgreSQL 13 and NOT PostgreSQL 15, got pg13=%v, pg15=%v", foundPg13_2021, foundPg15_2021)
	}

	// 3. Perform Time Travel RAG Query as of 2025 (ts2025)
	nodes2025, err := gllam.RetrieveHybridNeedleWithTime(ctx, "Primary Database version", []string{"node-db"}, "test-source", 10, &ts2025)
	if err != nil {
		t.Fatalf("Time travel query 2025 failed: %v", err)
	}

	var foundPg13_2025, foundPg15_2025 bool
	for _, sn := range nodes2025 {
		if sn.Node.ID == "node-db" {
			for _, l := range sn.Links {
				if l.TargetID == "node-pg13" {
					foundPg13_2025 = true
				}
				if l.TargetID == "node-pg15" {
					foundPg15_2025 = true
				}
			}
		}
	}
	if foundPg13_2025 || !foundPg15_2025 {
		t.Fatalf("Expected 2025 query to return active PostgreSQL 15 and NOT PostgreSQL 13, got pg13=%v, pg15=%v", foundPg13_2025, foundPg15_2025)
	}
}
