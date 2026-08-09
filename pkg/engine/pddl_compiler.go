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

	// Detect temporal cycles in graph edges
	cycleRes := DetectTemporalCycles(links)

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

	// If temporal cycles exist, inject explicit contradiction facts into initial state
	if cycleRes.HasCycle && len(cycleRes.CycleNodes) >= 2 {
		predicates["has_temporal_cycle"] = true
		c1 := cycleRes.CycleNodes[0]
		c2 := cycleRes.CycleNodes[1]
		initStatements = append(initStatements, fmt.Sprintf("    (has_temporal_cycle %s %s)", c1, c2))
	}

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

		// If a grounded temporal anchor ID and relation exist, emit grounded temporal predicates using Allen's Interval Algebra
		if link.TemporalAnchorID != "" && link.TemporalRelation != "" {
			anchor := SanitizePDDLName(link.TemporalAnchorID)
			tempRel := SanitizePDDLName(link.TemporalRelation)
			switch tempRel {
			case "before":
				tempRel = "happened_before"
			case "after":
				tempRel = "happened_after"
			case "during":
				tempRel = "during_interval"
			case "contains":
				tempRel = "contains_interval"
			case "equals", "overlaps", "starts", "finishes", "meets":
				// keep sanitized name as predicate
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
	if !predicates["happened_before"] {
		domain.WriteString("    (happened_before ?a ?b)\n")
	}
	if !predicates["happened_after"] {
		domain.WriteString("    (happened_after ?a ?b)\n")
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
    :effect (and (verified_sequence ?e1 ?e2) (happened_after ?e2 ?e1))
  )

  (:action verify_transitive_sequence
    :parameters (?e1 - event ?e2 - event ?e3 - event)
    :precondition (and (happened_before ?e1 ?e2) (happened_before ?e2 ?e3))
    :effect (and (happened_before ?e1 ?e3) (verified_sequence ?e1 ?e3) (happened_after ?e3 ?e1))
  )

  (:action verify_after
    :parameters (?e1 - event ?e2 - event)
    :precondition (and (happened_before ?e2 ?e1))
    :effect (and (happened_after ?e1 ?e2))
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
	problem.WriteString("(define (problem gllam_problem)\n")
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

	// 2. Check for "between X and Y" / "from X to Y" interval range queries
	if strings.Contains(promptLower, "between") || strings.Contains(promptLower, "from") {
		if len(matchedNodes) >= 2 {
			startNode := matchedNodes[0]
			endNode := matchedNodes[len(matchedNodes)-1]

			// Find any intermediate node E
			var interNode string
			for _, m := range matchedNodes[1 : len(matchedNodes)-1] {
				if m != startNode && m != endNode {
					interNode = m
					break
				}
			}
			if interNode == "" {
				for _, n := range nodes {
					sanitized := SanitizePDDLName(n.ID)
					if sanitized != startNode && sanitized != endNode {
						interNode = sanitized
						break
					}
				}
			}

			if interNode != "" {
				return fmt.Sprintf("(and (verified_sequence %s %s) (verified_sequence %s %s))", startNode, interNode, interNode, endNode)
			}
			return fmt.Sprintf("(and (verified_sequence %s %s))", startNode, endNode)
		}
	}

	// 3. Check for "since X" / "after X" interval queries
	if strings.Contains(promptLower, "since") {
		if len(matchedNodes) >= 1 {
			anchor := matchedNodes[0]
			var afterNode string
			for _, n := range nodes {
				sanitized := SanitizePDDLName(n.ID)
				if sanitized != anchor {
					afterNode = sanitized
					break
				}
			}
			if afterNode != "" {
				return fmt.Sprintf("(and (happened_after %s %s))", afterNode, anchor)
			}
		}
	}

	// 4. Check for "until Y" interval queries
	if strings.Contains(promptLower, "until") {
		if len(matchedNodes) >= 1 {
			anchor := matchedNodes[0]
			var beforeNode string
			for _, n := range nodes {
				sanitized := SanitizePDDLName(n.ID)
				if sanitized != anchor {
					beforeNode = sanitized
					break
				}
			}
			if beforeNode != "" {
				return fmt.Sprintf("(and (happened_before %s %s))", beforeNode, anchor)
			}
		}
	}

	// 5. Check for general temporal event ordering queries ("before", "after", "sequence", "order")
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

	// 6. Check for contradiction nodes
	for _, n := range nodes {
		if strings.ToLower(n.Type) == memory.NodeTypeContradiction {
			return fmt.Sprintf("(and (resolved %s))", SanitizePDDLName(n.ID))
		}
	}

	// 7. Fallback to verifying the primary retrieved link relationship
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

type CycleResult struct {
	HasCycle   bool
	CycleNodes []string
}

// DetectTemporalCycles performs Depth First Search to identify directed cycles in ordering links
func DetectTemporalCycles(links []memory.SemanticLink) CycleResult {
	adj := make(map[string][]string)

	for _, l := range links {
		src := SanitizePDDLName(l.SourceID)
		tgt := SanitizePDDLName(l.TargetID)
		rel := strings.ToLower(l.Relationship)
		tempRel := strings.ToLower(l.TemporalRelation)
		anchor := SanitizePDDLName(l.TemporalAnchorID)

		if rel == "happened_before" || tempRel == "before" {
			targetNode := tgt
			if anchor != "" && tempRel == "before" {
				targetNode = anchor
			}
			adj[src] = append(adj[src], targetNode)
		} else if rel == "happened_after" || tempRel == "after" {
			targetNode := tgt
			if anchor != "" && tempRel == "after" {
				targetNode = anchor
			}
			adj[targetNode] = append(adj[targetNode], src)
		}
	}

	visited := make(map[string]int) // 0 = unvisited, 1 = visiting (in stack), 2 = visited
	var cycleNodes []string

	var dfs func(node string, path []string) bool
	dfs = func(node string, path []string) bool {
		visited[node] = 1
		currentPath := append(path, node)

		for _, neighbor := range adj[node] {
			if visited[neighbor] == 1 {
				// Found cycle
				cycleStartIdx := -1
				for idx, p := range currentPath {
					if p == neighbor {
						cycleStartIdx = idx
						break
					}
				}
				if cycleStartIdx != -1 {
					cycleNodes = append(cycleNodes, currentPath[cycleStartIdx:]...)
				} else {
					cycleNodes = append(cycleNodes, neighbor, node)
				}
				return true
			} else if visited[neighbor] == 0 {
				if dfs(neighbor, currentPath) {
					return true
				}
			}
		}

		visited[node] = 2
		return false
	}

	for node := range adj {
		if visited[node] == 0 {
			if dfs(node, nil) {
				return CycleResult{HasCycle: true, CycleNodes: cycleNodes}
			}
		}
	}

	return CycleResult{HasCycle: false}
}




