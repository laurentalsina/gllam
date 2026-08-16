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

type PDDLAspect string

const (
	AspectAll             PDDLAspect = "all"
	AspectTemporal        PDDLAspect = "temporal"
	AspectInstruction     PDDLAspect = "instruction"
	AspectStateTransition PDDLAspect = "state_transition"
)

// ValidatePDDL performs lightweight structural validation of generated PDDL definitions
func ValidatePDDL(domain, problem string) error {
	if !strings.Contains(domain, "(define (domain") {
		return fmt.Errorf("invalid PDDL domain: missing (define (domain header")
	}
	if !strings.Contains(problem, "(define (problem") {
		return fmt.Errorf("invalid PDDL problem: missing (define (problem header")
	}
	if !strings.Contains(domain, "(:requirements") {
		return fmt.Errorf("invalid PDDL domain: missing (:requirements")
	}
	if !strings.Contains(domain, "(:predicates") {
		return fmt.Errorf("invalid PDDL domain: missing (:predicates")
	}
	if !strings.Contains(problem, "(:init") {
		return fmt.Errorf("invalid PDDL problem: missing (:init")
	}
	if !strings.Contains(problem, "(:goal") {
		return fmt.Errorf("invalid PDDL problem: missing (:goal")
	}
	return nil
}

// FilterNodesAndLinksForAspect projects a minimal sub-graph tailored to the requested aspect
func FilterNodesAndLinksForAspect(nodes []memory.SemanticNode, links []memory.SemanticLink, aspect PDDLAspect) ([]memory.SemanticNode, []memory.SemanticLink) {
	subNodesMap := make(map[string]memory.SemanticNode)
	var subLinks []memory.SemanticLink

	for _, l := range links {
		rel := strings.ToLower(l.Relationship)

		// Exclude fallacy subversion links from PDDL planning sub-graph
		if rel == "exhibits_fallacy" || rel == "subverts_claim" {
			continue
		}

		includeLink := false
		if aspect == AspectAll || aspect == "" {
			includeLink = true
		} else {
			switch aspect {
			case AspectTemporal:
				// Temporal fields moved to SemanticTemporalLink; no temporal filter on SemanticLink
				if rel == "happened_before" || rel == "happened_after" || rel == "during_interval" || rel == "contains_interval" {
					includeLink = true
				}
			case AspectInstruction:
				if rel == "has_constraint" || rel == "is_preference" || rel == "applies_rule" || rel == "supersedes_rule" || l.Modality == "deontic" {
					includeLink = true
				}
			case AspectStateTransition:
				if rel == "has_state" || rel == "causes" || rel == "introduced_state" {
					includeLink = true
				}
			}
		}

		if includeLink {
			subLinks = append(subLinks, l)
			for _, n := range nodes {
				if (n.ID == l.SourceID || n.ID == l.TargetID) && n.Type != memory.NodeTypeFallacy {
					subNodesMap[n.ID] = n
				}
			}
		}
	}

	// Fallback if pruning was too aggressive
	if len(subLinks) == 0 {
		return nodes, links
	}

	subNodes := make([]memory.SemanticNode, 0, len(subNodesMap))
	for _, n := range subNodesMap {
		subNodes = append(subNodes, n)
	}
	return subNodes, subLinks
}

// CompileGraphToPDDL takes semantic nodes and links and dynamically generates
// a typed PDDL domain and problem definition using default AspectAll.
func CompileGraphToPDDL(nodes []memory.SemanticNode, links []memory.SemanticLink, goalPredicate string, procedures []memory.ProceduralKnowledge) (string, string) {
	return CompileGraphToPDDLAspect(nodes, links, goalPredicate, procedures, AspectAll)
}

// CompileGraphToPDDLAspect dynamically projects sub-domain aspects and compiles typed PDDL definitions.
func CompileGraphToPDDLAspect(nodes []memory.SemanticNode, links []memory.SemanticLink, goalPredicate string, procedures []memory.ProceduralKnowledge, aspect PDDLAspect) (string, string) {
	// Project sub-graph according to requested aspect (isolating fallacies)
	nodes, links = FilterNodesAndLinksForAspect(nodes, links, aspect)

	// 1. Gather unique objects with types and dynamically infer predicates
	objectsByType := make(map[string][]string) // type -> list of sanitized node IDs
	nodeTypeMap := make(map[string]string)     // nodeID -> type
	predicates := make(map[string]bool)

	// Register explicit nodes (excluding fallacy nodes)
	for _, node := range nodes {
		if node.Type == memory.NodeTypeFallacy || strings.HasPrefix(strings.ToLower(node.ID), "fallacy_") {
			continue
		}
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

	// Map initial states and extract dynamic predicates from active semantic links
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

	}

	// 2. Build Domain String
	var domain strings.Builder
	domain.WriteString("(define (domain gllam)\n")
	domain.WriteString("  (:requirements :strips :typing)\n")
	domain.WriteString("  (:types event state entity service rule constraint human agent system contradiction - object)\n\n")

	domain.WriteString("  (:predicates\n")
	for pred := range predicates {
		domain.WriteString(fmt.Sprintf("    (%s ?a ?b)\n", pred))
	}
	// Standard predicates for temporal and instruction verification
	if !predicates["verified_sequence"] {
		domain.WriteString("    (verified_sequence ?a ?b)\n")
	}
	if !predicates["happened_before"] {
		domain.WriteString("    (happened_before ?a ?b)\n")
	}
	if !predicates["happened_after"] {
		domain.WriteString("    (happened_after ?a ?b)\n")
	}
	domain.WriteString("    (must_follow_rule ?r)\n")
	domain.WriteString("    (rule_satisfied ?r)\n")
	domain.WriteString("    (rule_violated ?r)\n")
	domain.WriteString("  )\n\n")

	// Inject dynamic (:action) blocks from ProceduralKnowledge or fallback temporal/rule actions
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

  (:action verify_instruction_rule
    :parameters (?r - object)
    :precondition (and (must_follow_rule ?r) (not (rule_violated ?r)))
    :effect (rule_satisfied ?r)
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

// ExtractPDDLGoalAndAspect derives both the PDDL goal expression and the target PDDLAspect sub-domain.
func ExtractPDDLGoalAndAspect(userPrompt string, nodes []memory.SemanticNode, links []memory.SemanticLink) (string, PDDLAspect) {
	goal := ExtractPDDLGoal(userPrompt, nodes, links)
	promptLower := strings.ToLower(userPrompt)

	if strings.Contains(promptLower, "rule") || strings.Contains(promptLower, "constraint") || strings.Contains(promptLower, "follow") || strings.Contains(promptLower, "format") || strings.Contains(promptLower, "preference") {
		return goal, AspectInstruction
	}
	if strings.Contains(promptLower, "state") || strings.Contains(promptLower, "version") || strings.Contains(promptLower, "causes") || strings.Contains(promptLower, "change") {
		return goal, AspectStateTransition
	}
	if strings.Contains(promptLower, "before") || strings.Contains(promptLower, "after") || strings.Contains(promptLower, "sequence") || strings.Contains(promptLower, "order") || strings.Contains(promptLower, "between") || strings.Contains(promptLower, "since") || strings.Contains(promptLower, "until") {
		return goal, AspectTemporal
	}
	return goal, AspectAll
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

	// 5. Check for rule/constraint verification queries ("rule", "constraint", "follow", "format", "preference")
	if strings.Contains(promptLower, "rule") || strings.Contains(promptLower, "constraint") || strings.Contains(promptLower, "follow") || strings.Contains(promptLower, "format") || strings.Contains(promptLower, "preference") {
		var ruleNode string
		for _, n := range nodes {
			if n.Type == memory.NodeTypeRule || n.Type == memory.NodeTypeConstraint || strings.HasPrefix(n.ID, "rule") || strings.HasPrefix(n.ID, "constraint") {
				ruleNode = SanitizePDDLName(n.ID)
				break
			}
		}
		if ruleNode != "" {
			return fmt.Sprintf("(and (rule_satisfied %s))", ruleNode)
		}
	}

	// 6. Check for general temporal event ordering queries ("before", "after", "sequence", "order")
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

		if rel == "happened_before" {
			adj[src] = append(adj[src], tgt)
		} else if rel == "happened_after" {
			adj[tgt] = append(adj[tgt], src)
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




