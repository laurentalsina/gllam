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
// a typed PDDL domain and problem definition.
func CompileGraphToPDDL(nodes []memory.SemanticNode, links []memory.SemanticLink, goalPredicate string, procedures []memory.ProceduralKnowledge) (string, string) {
	// 1. Gather unique objects with types and dynamically infer predicates
	objectsByType := make(map[string][]string) // type -> list of sanitized node IDs
	nodeTypeMap := make(map[string]string)     // nodeID -> type
	predicates := make(map[string]bool)

	// Register explicit nodes
	for _, node := range nodes {
		sanitizedID := SanitizePDDLName(node.ID)
		nodeType := strings.ToLower(node.Type)
		if nodeType == "" {
			nodeType = memory.NodeTypeEntity
		}
		if _, exists := nodeTypeMap[sanitizedID]; !exists {
			nodeTypeMap[sanitizedID] = nodeType
			objectsByType[nodeType] = append(objectsByType[nodeType], sanitizedID)
		}
	}

	var initStatements []string
	for _, link := range links {
		rel := SanitizePDDLName(link.Relationship)
		src := SanitizePDDLName(link.SourceID)
		tgt := SanitizePDDLName(link.TargetID)

		predicates[rel] = true

		// Ensure implicit nodes exist in the objects map if not explicitly passed
		if _, exists := nodeTypeMap[src]; !exists {
			nodeTypeMap[src] = memory.NodeTypeEntity
			objectsByType[memory.NodeTypeEntity] = append(objectsByType[memory.NodeTypeEntity], src)
		}
		if _, exists := nodeTypeMap[tgt]; !exists {
			nodeTypeMap[tgt] = memory.NodeTypeEntity
			objectsByType[memory.NodeTypeEntity] = append(objectsByType[memory.NodeTypeEntity], tgt)
		}

		// Create the PDDL initial state declaration
		initStatements = append(initStatements, fmt.Sprintf("    (%s %s %s)", rel, src, tgt))

		// If a grounded temporal anchor ID and relation exist, emit grounded temporal predicates
		if link.TemporalAnchorID != "" && link.TemporalRelation != "" {
			anchor := SanitizePDDLName(link.TemporalAnchorID)
			tempRel := SanitizePDDLName(link.TemporalRelation)
			if tempRel == "before" || tempRel == "happened_before" {
				tempRel = "happened_before"
			} else if tempRel == "after" || tempRel == "happened_after" {
				tempRel = "happened_after"
			}
			predicates[tempRel] = true
			if _, exists := nodeTypeMap[anchor]; !exists {
				nodeTypeMap[anchor] = memory.NodeTypeEntity
				objectsByType[memory.NodeTypeEntity] = append(objectsByType[memory.NodeTypeEntity], anchor)
			}
			initStatements = append(initStatements, fmt.Sprintf("    (%s %s %s)", tempRel, src, anchor))
		}

	}

	// 2. Build Domain String
	var domain strings.Builder
	domain.WriteString("(define (domain gllam)\n")
	domain.WriteString("  (:requirements :strips :typing)\n")
	domain.WriteString("  (:types event state entity service contradiction - object)\n\n")

	domain.WriteString("  (:predicates\n")
	for pred := range predicates {
		domain.WriteString(fmt.Sprintf("    (%s ?a ?b)\n", pred))
	}
	// Standard predicates for temporal verification
	if !predicates["verified_sequence"] {
		domain.WriteString("    (verified_sequence ?a ?b)\n")
	}
	domain.WriteString("  )\n\n")

	// Inject dynamic (:action) blocks from ProceduralKnowledge or fallback temporal actions
	hasExplicitActions := false
	for _, proc := range procedures {
		if strings.Contains(proc.Instructions, "(:action") {
			domain.WriteString(proc.Instructions)
			domain.WriteString("\n\n")
			hasExplicitActions = true
		}
	}

	if !hasExplicitActions {
		domain.WriteString(`  (:action sequence_events
    :parameters (?e1 - event ?e2 - event)
    :precondition (and (happened_before ?e1 ?e2))
    :effect (and (verified_sequence ?e1 ?e2))
  )

  (:action transition_state
    :parameters (?e - event ?s1 - state ?s2 - state)
    :precondition (and (has_state ?e ?s1) (causes ?e ?s2))
    :effect (and (not (has_state ?e ?s1)) (has_state ?e ?s2))
  )

`)
	}

	domain.WriteString(")\n")

	// 3. Build Problem String
	var problem strings.Builder
	problem.WriteString("(define (problem gllam_timeline)\n")
	problem.WriteString("  (:domain gllam)\n\n")

	problem.WriteString("  (:objects\n")
	for typeName, objList := range objectsByType {
		problem.WriteString(fmt.Sprintf("    %s - %s\n", strings.Join(objList, " "), typeName))
	}
	problem.WriteString("  )\n\n")

	problem.WriteString("  (:init\n")
	for _, initStmt := range initStatements {
		problem.WriteString(initStmt + "\n")
	}
	problem.WriteString("  )\n\n")

	if goalPredicate == "" {
		goalPredicate = "(and )"
	}
	problem.WriteString("  (:goal\n    " + goalPredicate + "\n  )\n")
	problem.WriteString(")\n")

	return domain.String(), problem.String()
}

// ExtractPDDLGoal dynamically derives a PDDL goal expression from a user prompt and retrieved context
func ExtractPDDLGoal(userPrompt string, nodes []memory.SemanticNode, links []memory.SemanticLink) string {
	promptLower := strings.ToLower(userPrompt)

	// 1. Entity name and ID matching against prompt
	matchedNodes := matchQueryEntities(userPrompt, nodes)

	// 2. Check for temporal event ordering queries ("before", "after", "sequence", "order")
	if strings.Contains(promptLower, "before") || strings.Contains(promptLower, "after") || strings.Contains(promptLower, "sequence") || strings.Contains(promptLower, "order") {
		// Prefer matched nodes if at least two were ground-matched in prompt
		if len(matchedNodes) >= 2 {
			if strings.Contains(promptLower, "after") && !strings.Contains(promptLower, "before") {
				return fmt.Sprintf("(and (verified_sequence %s %s))", matchedNodes[1], matchedNodes[0])
			}
			return fmt.Sprintf("(and (verified_sequence %s %s))", matchedNodes[0], matchedNodes[1])
		}

		// Fallback to event nodes in retrieved context
		var eventIDs []string
		for _, n := range nodes {
			if strings.ToLower(n.Type) == memory.NodeTypeEvent {
				eventIDs = append(eventIDs, SanitizePDDLName(n.ID))
			}
		}
		if len(eventIDs) >= 2 {
			return fmt.Sprintf("(and (verified_sequence %s %s))", eventIDs[0], eventIDs[1])
		}
	}

	// 3. Check for contradiction nodes
	for _, n := range nodes {
		if strings.ToLower(n.Type) == memory.NodeTypeContradiction {
			return fmt.Sprintf("(and (resolved %s))", SanitizePDDLName(n.ID))
		}
	}

	// 4. Fallback to verifying the primary retrieved link relationship
	if len(links) > 0 {
		rel := SanitizePDDLName(links[0].Relationship)
		src := SanitizePDDLName(links[0].SourceID)
		tgt := SanitizePDDLName(links[0].TargetID)
		return fmt.Sprintf("(and (%s %s %s))", rel, src, tgt)
	}

	return "(and )"
}

// matchQueryEntities matches substrings of node names and IDs within the prompt text
func matchQueryEntities(userPrompt string, nodes []memory.SemanticNode) []string {
	promptLower := strings.ToLower(userPrompt)
	var matched []string
	matchedMap := make(map[string]bool)

	for _, n := range nodes {
		sanitizedID := SanitizePDDLName(n.ID)
		nameLower := strings.ToLower(n.Name)
		idLower := strings.ToLower(n.ID)

		if (nameLower != "" && strings.Contains(promptLower, nameLower)) ||
			(idLower != "" && strings.Contains(promptLower, idLower)) {
			if !matchedMap[sanitizedID] {
				matchedMap[sanitizedID] = true
				matched = append(matched, sanitizedID)
			}
		}
	}

	return matched
}



