package engine

import (
    "context"
    "database/sql"
    "fmt"
    "strings"

    "github.com/laurentalsina/gllam/pkg/memory"
)


// RouteAndAssemble classifies the user prompt and assembles a structured context (read-only → dbRO)
func (e *GllamEngine) RouteAndAssemble(ctx context.Context, userPrompt string, entities []string) (*memory.CompiledContext, error) {
    ctxResult := &memory.CompiledContext{}

    // 1. Procedural Knowledge: Try Vector Search first, fallback to heuristic
    var procedures []memory.ProceduralKnowledge
    if e.embedder != nil {
        procs, err := e.SearchSimilarProcedures(ctx, userPrompt, 2)
        if err == nil && len(procs) > 0 {
            procedures = procs
        }
    }

    if len(procedures) == 0 {
        actionKeywords := []string{"how", "deploy", "fix", "build", "configure", "setup", "install", "create", "implement"}
        isProcedural := false
        for _, kw := range actionKeywords {
            if strings.Contains(strings.ToLower(userPrompt), kw) {
                isProcedural = true
                break
            }
        }
        if isProcedural {
            procs, err := e.GetTopProcedures(ctx, 3)
            if err == nil {
                procedures = procs
            }
        }
    }
    ctxResult.Procedural = procedures

    // 2. Semantic Entities: Auto-discover from prompt via vector search
    if e.embedder != nil {
        similarNodes, err := e.SearchSimilarNodes(ctx, userPrompt, 100)
        if err == nil {
            for _, node := range similarNodes {
                // Ensure we don't duplicate explicitly provided entities
                isDup := false
                for _, ent := range entities {
                    if ent == node.NodeID {
                        isDup = true
                        break
                    }
                }
                if !isDup {
                    entities = append(entities, node.NodeID)
                }
            }
        }
    }

    // Retrieve semantic links for requested and discovered entities
    if len(entities) > 0 {
        var links []memory.SemanticLink
        var nodes []memory.SemanticNode
        for _, entity := range entities {
            // Fetch node
            var node memory.SemanticNode
            var ctxPrompt *string
            err := e.dbRO.QueryRowContext(ctx, "SELECT id, name, type, context_prompt FROM semantic_nodes WHERE id = ?", entity).
                Scan(&node.ID, &node.Name, &node.Type, &ctxPrompt)
            if err == nil {
                if ctxPrompt != nil {
                    node.ContextPrompt = *ctxPrompt
                }
                nodes = append(nodes, node)
            }
            query := `
                SELECT source_id, target_id, relationship, caveats, valid_from, valid_until, temporal_anchor_id, temporal_relation, temporal_note, updated_at
                FROM semantic_links
                WHERE valid_until IS NULL AND (source_id = ? OR target_id = ?)
                LIMIT 15`

            rows, err := e.dbRO.QueryContext(ctx, query, entity, entity)
            if err != nil {
                return nil, fmt.Errorf("failed to query links for entity %s: %w", entity, err)
            }

            for rows.Next() {
                var link memory.SemanticLink
                var anchorID, tempRel, tempNote sql.NullString
                if err := rows.Scan(&link.SourceID, &link.TargetID, &link.Relationship, &link.Caveats,
                    &link.ValidFrom, &link.ValidUntil, &anchorID, &tempRel, &tempNote, &link.UpdatedAt); err != nil {
                    rows.Close()
                    return nil, fmt.Errorf("failed to scan link: %w", err)
                }
                if anchorID.Valid {
                    link.TemporalAnchorID = anchorID.String
                }
                if tempRel.Valid {
                    link.TemporalRelation = tempRel.String
                }
                if tempNote.Valid {
                    link.TemporalNote = tempNote.String
                }
                links = append(links, link)
            }
            rows.Close()


        }

        // Perform 2-hop temporal graph expansion to capture full transitive chains (A -> B -> C)
        expandedNodes, expandedLinks, err := e.ExpandTemporalNeighbors(ctx, nodes, links, 2)
        if err == nil {
            ctxResult.SemanticNodes = expandedNodes
            ctxResult.SemanticLinks = expandedLinks
        } else {
            ctxResult.SemanticLinks = links
            ctxResult.SemanticNodes = nodes
        }
    }


    // Append relevant episodic summaries
    var episodes []memory.EpisodicSummary
    if e.embedder != nil {
        eps, err := e.SearchSimilarEpisodes(ctx, userPrompt, 3)
        if err == nil {
            episodes = eps
        }
    } else {
        eps, err := e.GetRecentEpisodes(ctx, 3)
        if err == nil {
            episodes = eps
        }
    }
    ctxResult.Episodic = episodes

    // 4. Internal Cognitive Procedures
    // Collect unique triggers based on what was retrieved
    triggers := make(map[string]bool)
    for _, node := range ctxResult.SemanticNodes {
        triggers[node.Type] = true
    }
    // E.g., if we retrieved a 'contradiction' node, we look for a procedural recipe for it.
    
    for trigger := range triggers {
        procs, err := e.GetInternalProceduresByTrigger(ctx, "internal_semantic", trigger)
        if err == nil && len(procs) > 0 {
            ctxResult.Procedural = append(ctxResult.Procedural, procs...)
        }
    }

    // 5. Cognitive Trigger: PDDL Planning Engine for timelines and contradictions
    planningKeywords := []string{"before", "after", "timeline", "sequence", "possible", "could i have", "plan", "order"}
    requiresPlanning := false
    userPromptLower := strings.ToLower(userPrompt)
    for _, kw := range planningKeywords {
        if strings.Contains(userPromptLower, kw) {
            requiresPlanning = true
            break
        }
    }

    if requiresPlanning {
        // 1. Detect temporal cycles in retrieved semantic links
        cycleRes := DetectTemporalCycles(ctxResult.SemanticLinks)
        var cycleNotice string
        if cycleRes.HasCycle {
            cycleNotice = fmt.Sprintf("⚠️ TIMELINE CONTRADICTION DETECTED: A cyclic ordering dependency was found between nodes: %s.\n", strings.Join(cycleRes.CycleNodes, " -> "))
        }

        // 2. Dynamic PDDL Goal Extraction
        goalPredicate := ExtractPDDLGoal(userPrompt, ctxResult.SemanticNodes, ctxResult.SemanticLinks)

        // 3. Compile the retrieved graph into PDDL strings using our typed compiler
        domainStr, problemStr := CompileGraphToPDDL(ctxResult.SemanticNodes, ctxResult.SemanticLinks, goalPredicate, ctxResult.Procedural)

        // 4. Invoke the dual-tier planning engine
        planner := NewNativePlanner()
        _, err := planner.Solve(ctx, domainStr, problemStr)
        if err != nil {
            if e.PlannerExecutablePath != "" {
                extPlanner := NewFastDownwardPlanner(e.PlannerExecutablePath)
                extPlan, extErr := extPlanner.Solve(ctx, domainStr, problemStr)
                if extErr != nil {
                    if cycleRes.HasCycle {
                        ctxResult.PlannerOutput = fmt.Sprintf("%sPlanning Engine failed to resolve sequence due to temporal cycle contradiction.\nExtracted Goal: %s", cycleNotice, goalPredicate)
                    } else {
                        diag := FormatUnsolvableDiagnostic(goalPredicate, ctxResult.SemanticLinks)
                        ctxResult.PlannerOutput = fmt.Sprintf("%s\nExtracted Goal: %s", diag, goalPredicate)
                    }
                } else {
                    ctxResult.PlannerOutput = fmt.Sprintf("%sPlanning Engine triggered via External PDDL Planner. Plan length: %d actions.", cycleNotice, len(extPlan))
                }
            } else {
                if cycleRes.HasCycle {
                    ctxResult.PlannerOutput = fmt.Sprintf("%sPlanning Engine failed to resolve sequence due to temporal cycle contradiction.\nExtracted Goal: %s", cycleNotice, goalPredicate)
                } else {
                    diag := FormatUnsolvableDiagnostic(goalPredicate, ctxResult.SemanticLinks)
                    ctxResult.PlannerOutput = fmt.Sprintf("%s\nExtracted Goal: %s", diag, goalPredicate)
                }
            }
        } else {
            ctxResult.PlannerOutput = fmt.Sprintf("%sPlanning Engine triggered. Sequence mathematically verified.", cycleNotice)
        }
    }



    return ctxResult, nil
}


// FormatSystemPrompt formats the compiled context into a Markdown block for LLM consumption
func FormatSystemPrompt(ctx *memory.CompiledContext) string {
    var sb strings.Builder

    sb.WriteString("# GLLAM Context\n\n")

    // Dynamic warning if conflicts are present in the retrieved graph
    if len(ctx.SemanticLinks) > 0 {
        for _, link := range ctx.SemanticLinks {
            if link.Relationship == "has_unresolved_conflict" {
                sb.WriteString("⚠️ Warning: The semantic graph contains unresolved conflicts. Please ask the user to clarify which conflicting claim is correct.\n\n")
                break
            }
        }
    }

    // Procedural knowledge
    if len(ctx.Procedural) > 0 {
        sb.WriteString("## Procedural Knowledge\n\n")
        for _, pk := range ctx.Procedural {
            sb.WriteString(fmt.Sprintf("### %s (v%d)\n\n", pk.TaskType, pk.Version))
            sb.WriteString(pk.Instructions + "\n\n")
            if pk.UserFeedbackRules != "" {
                sb.WriteString(fmt.Sprintf("**User Rules:** %s\n\n", pk.UserFeedbackRules))
            }
            sb.WriteString("---\n\n")
        }
    }

    // Entity Profiles (Context)
    if len(ctx.SemanticNodes) > 0 {
        hasContext := false
        for _, node := range ctx.SemanticNodes {
            if node.ContextPrompt != "" {
                if !hasContext {
                    sb.WriteString("## Entity Profiles\n\n")
                    hasContext = true
                }
                sb.WriteString(fmt.Sprintf("### %s (%s)\n%s\n\n", node.Name, node.Type, node.ContextPrompt))
            }
        }
    }

    // Semantic links
    if len(ctx.SemanticLinks) > 0 {
        sb.WriteString("## Semantic Graph\n\n")
        for _, link := range ctx.SemanticLinks {
            sb.WriteString(fmt.Sprintf("- (%s) -[%s]-> (%s)\n",
                link.SourceID, link.Relationship, link.TargetID))
            if link.Caveats != "" {
                sb.WriteString(fmt.Sprintf("  - Caveat: %s\n", link.Caveats))
            }
        }
        sb.WriteString("\n")
    }

    // Episodic summaries
    if len(ctx.Episodic) > 0 {
        sb.WriteString("## Recent Episodes\n\n")
        for _, ep := range ctx.Episodic {
            sb.WriteString(fmt.Sprintf("- [%s] %s\n", formatTimestamp(ep.CreatedAt), ep.SummaryText))
        }
        sb.WriteString("\n")
    }

    // PDDL Planner Output
    if ctx.PlannerOutput != "" {
        sb.WriteString("## Mathematical Sequence Verification (PDDL)\n\n")
        sb.WriteString(ctx.PlannerOutput)
        sb.WriteString("\n\n")
    }

    return sb.String()
}

// FormatUnsolvableDiagnostic analyzes why PDDL solvers failed to find a plan for a goal
func FormatUnsolvableDiagnostic(goalPredicate string, links []memory.SemanticLink) string {
	if strings.Contains(goalPredicate, "verified_sequence") || strings.Contains(goalPredicate, "happened_before") {
		parts := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(goalPredicate, "(", " "), ")", " "))
		if len(parts) >= 3 {
			src := parts[len(parts)-2]
			tgt := parts[len(parts)-1]

			for _, l := range links {
				lSrc := SanitizePDDLName(l.SourceID)
				lTgt := SanitizePDDLName(l.TargetID)
				lRel := strings.ToLower(l.Relationship)
				lAnchor := SanitizePDDLName(l.TemporalAnchorID)
				lTempRel := strings.ToLower(l.TemporalRelation)

				if (lSrc == tgt && lTgt == src && lRel == "happened_before") ||
					(lSrc == tgt && lAnchor == src && lTempRel == "before") ||
					(lSrc == src && lTgt == tgt && lRel == "happened_after") {
					return fmt.Sprintf("⚠️ TIMELINE CONTRADICTION: Requested sequence '%s before %s' is mathematically impossible because the graph records '%s occurred before %s'.", src, tgt, tgt, src)
				}
			}

			return fmt.Sprintf("⚠️ TIMELINE UNPROVABLE: Requested sequence '%s before %s' cannot be verified from recorded graph links (insufficient causal/ordering links).", src, tgt)
		}
	}

	return fmt.Sprintf("⚠️ TIMELINE UNPROVABLE: Goal predicate %s could not be verified by the planning engine.", goalPredicate)
}


// formatTimestamp converts a Unix timestamp to a readable format
func formatTimestamp(ts int64) string {
    return fmt.Sprintf("%d", ts)
}
