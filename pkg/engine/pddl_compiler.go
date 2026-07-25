package engine

import (
	"fmt"
	"strings"

	"github.com/laurentalsina/gllam/pkg/memory"
)

// SanitizePDDLName ensures identifiers are safe for strict PDDL syntax
func SanitizePDDLName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, ".", "_")
	return name
}

// CompileGraphToPDDL takes semantic nodes and links and dynamically generates
// a PDDL domain and problem definition.
func CompileGraphToPDDL(nodes []memory.SemanticNode, links []memory.SemanticLink, goalPredicate string) (string, string) {
	// 1. Gather unique objects and dynamically infer predicates
	objects := make(map[string]bool)
	predicates := make(map[string]bool)

	for _, node := range nodes {
		objects[SanitizePDDLName(node.ID)] = true
	}

	var initStatements []string
	for _, link := range links {
		rel := SanitizePDDLName(link.Relationship)
		src := SanitizePDDLName(link.SourceID)
		tgt := SanitizePDDLName(link.TargetID)

		predicates[rel] = true

		// Ensure implicit nodes exist in the objects list just in case
		// they weren't passed in the explicit node list
		objects[src] = true
		objects[tgt] = true

		// Create the PDDL initial state declaration
		initStatements = append(initStatements, fmt.Sprintf("    (%s %s %s)", rel, src, tgt))
	}

	// 2. Build Domain String
	var domain strings.Builder
	domain.WriteString("(define (domain gllam)\n")
	domain.WriteString("  (:requirements :strips :typing)\n")
	domain.WriteString("  (:types entity)\n")

	domain.WriteString("  (:predicates\n")
	for pred := range predicates {
		// We treat all nodes generically as 'entity' for maximum flexibility right now
		domain.WriteString(fmt.Sprintf("    (%s ?a - entity ?b - entity)\n", pred))
	}
	domain.WriteString("  )\n\n")

	// TODO: Loop through memory.ProceduralKnowledge and inject (:action) blocks here.
	// For GLLAM 0.2, this is where the dynamic recipes will live.
	domain.WriteString("  ;; Dynamic Procedural Actions will be injected here\n")

	domain.WriteString(")\n")

	// 3. Build Problem String
	var problem strings.Builder
	problem.WriteString("(define (problem gllam_timeline)\n")
	problem.WriteString("  (:domain gllam)\n\n")

	problem.WriteString("  (:objects\n    ")
	for obj := range objects {
		problem.WriteString(fmt.Sprintf("%s ", obj))
	}
	problem.WriteString("- entity\n  )\n\n")

	problem.WriteString("  (:init\n")
	for _, initStmt := range initStatements {
		problem.WriteString(initStmt + "\n")
	}
	problem.WriteString("  )\n\n")

	if goalPredicate == "" {
		// Fallback empty goal if none specified
		goalPredicate = "(and )"
	}
	problem.WriteString("  (:goal\n    " + goalPredicate + "\n  )\n")
	problem.WriteString(")\n")

	return domain.String(), problem.String()
}
