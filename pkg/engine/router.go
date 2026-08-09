package engine

import (
    "context"
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

    // Retrieve semantic links and nodes via Dual-Channel RRF Hybrid Retrieval
    if len(entities) > 0 || userPrompt != "" {
        needleResults, err := e.RetrieveHybridNeedle(ctx, userPrompt, entities, "", 10)
        if err == nil && len(needleResults) > 0 {
            nodeMap := make(map[string]memory.SemanticNode)
            linkMap := make(map[string]memory.SemanticLink)

            for _, nr := range needleResults {
                nodeMap[nr.Node.ID] = nr.Node
                for _, l := range nr.Links {
                    key := fmt.Sprintf("%s-%s-%s", l.SourceID, l.TargetID, l.Relationship)
                    linkMap[key] = l
                }
            }

            var hybridNodes []memory.SemanticNode
            for _, n := range nodeMap {
                hybridNodes = append(hybridNodes, n)
            }
            var hybridLinks []memory.SemanticLink
            for _, l := range linkMap {
                hybridLinks = append(hybridLinks, l)
            }

            // Perform 2-hop temporal graph expansion to capture full transitive chains
            expandedNodes, expandedLinks, expErr := e.ExpandTemporalNeighbors(ctx, hybridNodes, hybridLinks, 2)
            if expErr == nil {
                ctxResult.SemanticNodes = expandedNodes
                ctxResult.SemanticLinks = expandedLinks
            } else {
                ctxResult.SemanticNodes = hybridNodes
                ctxResult.SemanticLinks = hybridLinks
            }

            // Evaluate Quantitative Constraints (Trap 2)
            var proposedCost float64
            userPromptLower := strings.ToLower(userPrompt)
            if strings.Contains(userPromptLower, "can i buy") || strings.Contains(userPromptLower, "afford") || strings.Contains(userPromptLower, "cost") {
                var cost float64
                if _, err := fmt.Sscanf(userPrompt, "can i buy for %f", &cost); err == nil {
                    proposedCost = cost
                } else if _, err := fmt.Sscanf(userPrompt, "can i buy something for %f", &cost); err == nil {
                    proposedCost = cost
                }
            }

            if proposedCost > 0 {
                quantRes := EvaluateQuantitativeConstraints(ctxResult.SemanticNodes, ctxResult.SemanticLinks, proposedCost)
                if quantRes.Explanation != "" {
                    if ctxResult.PlannerOutput != "" {
                        ctxResult.PlannerOutput += "\n" + quantRes.Explanation
                    } else {
                        ctxResult.PlannerOutput = quantRes.Explanation
                    }
                }
            }
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

    // Check for Byzantine Fallacy Subversion
    if len(ctxResult.SemanticLinks) > 0 || len(ctxResult.SemanticNodes) > 0 {
        fallacyDiag := DetectFallacySubversion(ctxResult.SemanticLinks, ctxResult.SemanticNodes)
        if fallacyDiag != "" {
            if ctxResult.PlannerOutput != "" {
                ctxResult.PlannerOutput += "\n" + fallacyDiag
            } else {
                ctxResult.PlannerOutput = fallacyDiag
            }
        }
    }






    // 6. Fetch Document Lineage for retrieved semantic nodes (Issue #8 Strict Information Lineage)
    if len(ctxResult.SemanticNodes) > 0 {
        var nodeIDs []string
        for _, n := range ctxResult.SemanticNodes {
            nodeIDs = append(nodeIDs, n.ID)
        }
        lineageRecords, err := e.GetDocumentLineageForNodes(ctx, nodeIDs)
        if err == nil && len(lineageRecords) > 0 {
            ctxResult.Lineage = lineageRecords
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

    // Strict Source Lineage Citations (Issue #8 & Multi-Author Versions)
    if len(ctx.Lineage) > 0 {
        sb.WriteString("## Strict Source Lineage Citations\n\n")
        sb.WriteString("When synthesizing facts from this context, you MUST explicitly cite source URIs and author provenance:\n\n")
        for _, lin := range ctx.Lineage {
            lineStr := ""
            if lin.LineNumber > 0 {
                lineStr = fmt.Sprintf(" (Line %d)", lin.LineNumber)
            }
            titleStr := ""
            if lin.DocumentTitle != "" {
                titleStr = fmt.Sprintf(" - %s", lin.DocumentTitle)
            }
            authorStr := ""
            if len(lin.Authors) > 0 {
                authorStr = fmt.Sprintf(" [Authors: %s]", strings.Join(lin.Authors, ", "))
            }
            sb.WriteString(fmt.Sprintf("- Node `%s` [%s] %s%s%s%s\n", lin.NodeID, lin.SourceType, lin.SourceURI, titleStr, lineStr, authorStr))

            // Sub-bullet version history edits
            if len(lin.Versions) > 0 {
                for _, v := range lin.Versions {
                    vAuthor := v.AuthorID
                    if v.AuthorName != "" {
                        vAuthor = fmt.Sprintf("%s (%s)", v.AuthorName, v.AuthorID)
                    }
                    lines := ""
                    if v.StartLine > 0 && v.EndLine > 0 {
                        lines = fmt.Sprintf(" Lines %d-%d", v.StartLine, v.EndLine)
                    }
                    sb.WriteString(fmt.Sprintf("  * v%d by %s%s: %s\n", v.VersionNumber, vAuthor, lines, v.ChangeSummary))
                }
            }
        }
        sb.WriteString("\n")
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
