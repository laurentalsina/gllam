package engine

import (
    "context"
    "database/sql"
    "fmt"
    "regexp"
    "strings"

    "github.com/laurentalsina/gllam/pkg/memory"
)



// RouteAndAssemble classifies the user prompt and assembles a structured context (read-only → dbRO)
func (e *GllamEngine) RouteAndAssemble(ctx context.Context, userPrompt string, entities []string) (*memory.CompiledContext, error) {
    _ = e.DecrementActiveTurnConstraints(ctx)
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
                SELECT source_id, target_id, relationship, caveats, valid_from, valid_until, temporal_anchor_id, temporal_relation, temporal_offset_seconds, temporal_granularity, temporal_note, origin_source_id, rule_context, constraint_type, rule_rationale, resolution_rationale, duration_turns, remaining_turns, updated_at
                FROM semantic_links
                WHERE valid_until IS NULL AND (source_id = ? OR target_id = ?)
                LIMIT 15`

            rows, err := e.dbRO.QueryContext(ctx, query, entity, entity)
            if err != nil {
                return nil, fmt.Errorf("failed to query links for entity %s: %w", entity, err)
            }

            for rows.Next() {
                var link memory.SemanticLink
                var anchorID, tempRel, tempGran, tempNote, origSource, rCtx, cType, ratVal, resRatVal sql.NullString
                var durTurns, remTurns sql.NullInt64
                if err := rows.Scan(&link.SourceID, &link.TargetID, &link.Relationship, &link.Caveats,
                    &link.ValidFrom, &link.ValidUntil, &anchorID, &tempRel, &link.TemporalOffsetSeconds, &tempGran, &tempNote, &origSource, &rCtx, &cType, &ratVal, &resRatVal, &durTurns, &remTurns, &link.UpdatedAt); err != nil {
                    rows.Close()
                    return nil, fmt.Errorf("failed to scan link: %w", err)
                }
                if anchorID.Valid {
                    link.TemporalAnchorID = anchorID.String
                }
                if tempRel.Valid {
                    link.TemporalRelation = tempRel.String
                }
                if tempGran.Valid {
                    link.TemporalGranularity = tempGran.String
                }
                if tempNote.Valid {
                    link.TemporalNote = tempNote.String
                }
                if origSource.Valid {
                    link.OriginSourceID = origSource.String
                }
                if rCtx.Valid {
                    link.RuleContext = rCtx.String
                }
                if cType.Valid {
                    link.ConstraintType = cType.String
                }
                if ratVal.Valid {
                    link.RuleRationale = ratVal.String
                }
                if resRatVal.Valid {
                    link.ResolutionRationale = resRatVal.String
                }
                if durTurns.Valid {
                    link.DurationTurns = durTurns.Int64
                } else {
                    link.DurationTurns = -1
                }
                if remTurns.Valid {
                    link.RemainingTurns = remTurns.Int64
                } else {
                    link.RemainingTurns = -1
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

    // 5. Cognitive Trigger: PDDL Planning Engine for timelines, contradictions, rules, and formats
    planningKeywords := []string{"before", "after", "timeline", "sequence", "possible", "could i have", "plan", "order", "rule", "constraint", "follow", "format", "preference"}
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

        // 2. Dynamic PDDL Goal & Aspect Extraction
        goalPredicate, aspect := ExtractPDDLGoalAndAspect(userPrompt, ctxResult.SemanticNodes, ctxResult.SemanticLinks)

        // 3. Compile the retrieved graph into sub-domain PDDL strings using aspect projection
        domainStr, problemStr := CompileGraphToPDDLAspect(ctxResult.SemanticNodes, ctxResult.SemanticLinks, goalPredicate, ctxResult.Procedural, aspect)

        // 4. Validate PDDL schema before execution
        if valErr := ValidatePDDL(domainStr, problemStr); valErr != nil {
            ctxResult.PlannerOutput = fmt.Sprintf("⚠️ PDDL Validation Warning: %v\nExtracted Goal: %s", valErr, goalPredicate)
        } else {
            // 5. Invoke the dual-tier planning engine
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
                        ctxResult.PlannerOutput = fmt.Sprintf("%sPlanning Engine triggered via External PDDL Planner [%s aspect]. Plan length: %d actions.", cycleNotice, aspect, len(extPlan))
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
                ctxResult.PlannerOutput = fmt.Sprintf("%sPlanning Engine triggered [%s aspect]. Sequence mathematically verified.", cycleNotice, aspect)
            }
        }
    }

    // Check for rule rationale confrontations
    if len(ctxResult.SemanticLinks) > 0 {
        confrontationDiag := ConfrontRuleRationales(ctxResult.SemanticLinks)
        if confrontationDiag != "" {
            if ctxResult.PlannerOutput != "" {
                ctxResult.PlannerOutput += "\n" + confrontationDiag
            } else {
                ctxResult.PlannerOutput = confrontationDiag
            }
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

    rawText := sb.String()
    return RedactProhibitedContent(rawText, ctx.SemanticLinks, ctx.SemanticNodes)
}

var (
    ipRegex    = regexp.MustCompile(`\b(?:10|172\.(?:1[6-9]|2[0-9]|3[01])|192\.168)\.\d{1,3}\.\d{1,3}\b`)
    tokenRegex = regexp.MustCompile(`(?i)\b(bearer\s+[A-Za-z0-9\-\._~\+\/]+=*|sk-[A-Za-z0-9]{16,}|ghp_[A-Za-z0-9]{20,})\b`)
)

// RedactProhibitedContent applies regex and semantic negative constraint redactions to text
func RedactProhibitedContent(text string, links []memory.SemanticLink, nodes []memory.SemanticNode) string {
    hasNegativeConstraint := false
    for _, l := range links {
        if l.ConstraintType == "negative" || strings.Contains(strings.ToLower(l.TargetID), "no_") || strings.Contains(strings.ToLower(l.TargetID), "never_") || strings.Contains(strings.ToLower(l.TargetID), "dont_") {
            hasNegativeConstraint = true
            targetLower := strings.ToLower(l.TargetID)

            // Redact IP addresses if constraint mentions IP
            if strings.Contains(targetLower, "ip") || strings.Contains(targetLower, "internal_ip") {
                text = ipRegex.ReplaceAllString(text, "[REDACTED_INTERNAL_IP]")
            }
            // Redact tokens/passwords if constraint mentions token/password/auth/secret
            if strings.Contains(targetLower, "token") || strings.Contains(targetLower, "password") || strings.Contains(targetLower, "secret") || strings.Contains(targetLower, "auth") {
                text = tokenRegex.ReplaceAllString(text, "[REDACTED_SECRET]")
            }

            // If negative constraint points to a specific node, redact that node's name or ID mentions
            for _, n := range nodes {
                if n.ID == l.TargetID && n.Name != "" {
                    nodeNameRegex := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(n.Name) + `\b`)
                    text = nodeNameRegex.ReplaceAllString(text, "[REDACTED_RESTRICTED_ENTITY]")
                }
            }
        }
    }

    // Default safety pass if any negative constraint is present
    if hasNegativeConstraint {
        text = ipRegex.ReplaceAllString(text, "[REDACTED_INTERNAL_IP]")
        text = tokenRegex.ReplaceAllString(text, "[REDACTED_SECRET]")
    }

    return text
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
