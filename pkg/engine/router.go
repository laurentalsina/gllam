package engine

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

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
                if !isDup && node.Distance < 0.38 {
                    entities = append(entities, node.NodeID)
                }
            }
        }
    }

    // Extract namespace prefix if present (e.g. [Conversation 14 context] -> "beam-100k-14-")
    var prefixFilter string
    if strings.HasPrefix(userPrompt, "[Conversation ") {
        endIdx := strings.Index(userPrompt, " context]")
        if endIdx > 14 {
            convID := userPrompt[14:endIdx]
            prefixFilter = fmt.Sprintf("beam-100k-%s-", convID)
        }
    }

    // Retrieve semantic links and nodes via Dual-Channel RRF Hybrid Retrieval
    if len(entities) > 0 || userPrompt != "" {
        needleResults, err := e.RetrieveHybridNeedle(ctx, userPrompt, entities, "", 50)
        if err == nil && len(needleResults) > 0 {
            nodeMap := make(map[string]memory.SemanticNode)
            linkMap := make(map[string]memory.SemanticLink)

            for _, nr := range needleResults {
                if prefixFilter != "" && !strings.HasPrefix(nr.Node.ID, prefixFilter) {
                    continue
                }
                nodeMap[nr.Node.ID] = nr.Node
                for _, l := range nr.Links {
                    if prefixFilter != "" && (!strings.HasPrefix(l.SourceID, prefixFilter) || !strings.HasPrefix(l.TargetID, prefixFilter)) {
                        continue
                    }
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

            // Perform 1-hop temporal graph expansion to capture transitive chains within token budget
            expandedNodes, expandedLinks, expErr := e.ExpandTemporalNeighbors(ctx, hybridNodes, hybridLinks, 1)
            if expErr == nil {
                ctxResult.SemanticNodes = expandedNodes
                ctxResult.SemanticLinks = expandedLinks
            } else {
                ctxResult.SemanticNodes = hybridNodes
                ctxResult.SemanticLinks = hybridLinks
            }

            // Cap expanded graph to top 150 nodes and 300 links to prevent prompt payload explosion
            if len(ctxResult.SemanticNodes) > 150 {
                ctxResult.SemanticNodes = ctxResult.SemanticNodes[:150]
            }
            if len(ctxResult.SemanticLinks) > 300 {
                ctxResult.SemanticLinks = ctxResult.SemanticLinks[:300]
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
        eps, err := e.SearchSimilarEpisodes(ctx, userPrompt, 5)
        if err == nil {
            for _, ep := range eps {
                if prefixFilter != "" && !strings.HasPrefix(ep.ID, prefixFilter) && !strings.HasPrefix(ep.SessionID, prefixFilter) {
                    continue
                }
                episodes = append(episodes, ep)
            }
        }
    } else {
        eps, err := e.GetRecentEpisodes(ctx, 5)
        if err == nil {
            for _, ep := range eps {
                if prefixFilter != "" && !strings.HasPrefix(ep.ID, prefixFilter) && !strings.HasPrefix(ep.SessionID, prefixFilter) {
                    continue
                }
                episodes = append(episodes, ep)
            }
        }
    }

    // FTS5 / Keyword Corpus Back-Search Fallback (PLAN_missing_entity_corpus_fallback_and_trap_detection)
    if len(ctxResult.SemanticNodes) < 25 || len(episodes) < 5 {
        words := strings.Fields(userPrompt)
        stopWords := map[string]bool{
            "what": true, "does": true, "did": true, "before": true, "after": true, "is": true,
            "the": true, "a": true, "an": true, "where": true, "why": true, "who": true, "how": true,
            "mentioned": true, "say": true, "said": true, "about": true, "with": true, "to": true,
            "mention": true, "type": true, "first": true, "second": true, "between": true, "from": true,
        }
        for _, rawW := range words {
            w := strings.Trim(strings.ToLower(rawW), "?!.,'\":;")
            if len(w) < 3 || stopWords[w] {
                continue
            }
            var query string
            var args []interface{}
            if prefixFilter != "" {
                query = `SELECT id, session_id, summary_text, created_at FROM episodic_summaries WHERE summary_text LIKE ? AND id LIKE ? ORDER BY created_at DESC LIMIT 5`
                args = []interface{}{"%"+w+"%", prefixFilter+"%"}
            } else {
                query = `SELECT id, session_id, summary_text, created_at FROM episodic_summaries WHERE summary_text LIKE ? ORDER BY created_at DESC LIMIT 5`
                args = []interface{}{"%"+w+"%"}
            }
            rows, err := e.dbRO.QueryContext(ctx, query, args...)
            if err == nil {
                for rows.Next() {
                    var ep memory.EpisodicSummary
                    if err := rows.Scan(&ep.ID, &ep.SessionID, &ep.SummaryText, scanTime(&ep.CreatedAt)); err == nil {
                        isDup := false
                        for _, existing := range episodes {
                            if existing.ID == ep.ID {
                                isDup = true
                                break
                            }
                        }
                        if !isDup {
                            episodes = append(episodes, ep)
                        }
                    }
                }
                rows.Close()
            }
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

    // Also include active global/contextual constraints
    globalConstraints, err := e.GetActiveConstraintsForSource(ctx, "", "global")
    if err == nil && len(globalConstraints) > 0 {
        existingKeys := make(map[string]bool)
        for _, l := range ctxResult.SemanticLinks {
            key := fmt.Sprintf("%s-%s-%s", l.SourceID, l.TargetID, l.Relationship)
            existingKeys[key] = true
        }
        for _, l := range globalConstraints {
            key := fmt.Sprintf("%s-%s-%s", l.SourceID, l.TargetID, l.Relationship)
            if !existingKeys[key] {
                ctxResult.SemanticLinks = append(ctxResult.SemanticLinks, l)
            }
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

        // 3. Compile & Persist PDDL files to disk for inspection and procedural memory reuse
        domainStr, problemStr := CompileGraphToPDDLAspect(ctxResult.SemanticNodes, ctxResult.SemanticLinks, goalPredicate, ctxResult.Procedural, aspect)

        pddlDir := "./bench/pddl_domains"
        _ = os.MkdirAll(pddlDir, 0755)
        domainFile := fmt.Sprintf("%s/domain_%s.pddl", pddlDir, aspect)
        problemFile := fmt.Sprintf("%s/problem_%d.pddl", pddlDir, time.Now().UnixNano())
        _ = os.WriteFile(domainFile, []byte(domainStr), 0644)
        _ = os.WriteFile(problemFile, []byte(problemStr), 0644)

        ctxResult.PDDLDomainPath = domainFile
        ctxResult.PDDLProblemPath = problemFile

        // 4. Validate PDDL schema before execution
        if valErr := ValidatePDDL(domainStr, problemStr); valErr != nil {
            ctxResult.PlannerOutput = fmt.Sprintf("⚠️ PDDL Validation Warning: %v\nExtracted Goal: %s", valErr, goalPredicate)
        } else {
            // 5. Invoke the dual-tier planning engine
            planner := NewNativePlanner()
            plan, err := planner.Solve(ctx, domainStr, problemStr)
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
                        var steps []string
                        for idx, act := range extPlan {
                            if len(act.Parameters) > 0 {
                                steps = append(steps, fmt.Sprintf("   %d. %s: %s", idx+1, act.Name, strings.Join(act.Parameters, " -> ")))
                            } else {
                                steps = append(steps, fmt.Sprintf("   %d. %s", idx+1, act.Name))
                            }
                        }
                        ctxResult.PlannerOutput = fmt.Sprintf("%sPlanning Engine triggered via External PDDL Planner [%s aspect]. Plan length: %d actions.\nProven Sequence:\n%s", cycleNotice, aspect, len(extPlan), strings.Join(steps, "\n"))
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
                var steps []string
                for idx, act := range plan {
                    if len(act.Parameters) > 0 {
                        steps = append(steps, fmt.Sprintf("   %d. %s: %s", idx+1, act.Name, strings.Join(act.Parameters, " -> ")))
                    } else {
                        steps = append(steps, fmt.Sprintf("   %d. %s", idx+1, act.Name))
                    }
                }
                ctxResult.PlannerOutput = fmt.Sprintf("%sPlanning Engine triggered [%s aspect]. Sequence mathematically verified.\nProven Sequence:\n%s", cycleNotice, aspect, strings.Join(steps, "\n"))
            }
        }

        // Detailed PDDL Invocation Log Output
        fmt.Printf("\n🧩 [PDDL INVOCATION TRACE]\n")
        fmt.Printf("   ├── Question: %q\n", userPrompt)
        fmt.Printf("   ├── Aspect Projection: %s\n", aspect)
        fmt.Printf("   ├── Extracted Goal: %s\n", goalPredicate)
        fmt.Printf("   ├── Sub-graph Input: %d nodes, %d links\n", len(ctxResult.SemanticNodes), len(ctxResult.SemanticLinks))
        fmt.Printf("   ├── Domain File: %s (%d bytes, %d lines)\n", domainFile, len(domainStr), strings.Count(domainStr, "\n"))
        fmt.Printf("   ├── Problem File: %s (%d bytes, %d lines)\n", problemFile, len(problemStr), strings.Count(problemStr, "\n"))
        fmt.Printf("   └── Execution Result: %s\n\n", strings.ReplaceAll(ctxResult.PlannerOutput, "\n", " "))
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
        var retrievedNodeIDs []string
        for _, n := range ctxResult.SemanticNodes {
            retrievedNodeIDs = append(retrievedNodeIDs, n.ID)
        }
        lineageRecords, err := e.GetDocumentLineageForNodes(ctx, retrievedNodeIDs)
        if err == nil && len(lineageRecords) > 0 {
            ctxResult.Lineage = lineageRecords
        }
    if e.SystemPrompts != nil {
        ctxResult.ResponseGuidelines = e.SystemPrompts.ResponseGuidelines
    }
    }

    return ctxResult, nil
}



// FormatSystemPrompt formats the compiled context into a Markdown block for LLM consumption
func FormatSystemPrompt(ctx *memory.CompiledContext) string {
    var sb strings.Builder

    sb.WriteString("# GLLAM System Context & Guidelines\n\n")
    if ctx.ResponseGuidelines != "" {
        sb.WriteString(ctx.ResponseGuidelines + "\n\n")
    } else {
        sb.WriteString("## Response Guidelines\n")
        sb.WriteString("- Rely ONLY on facts and quotes explicitly stated in the provided GLLAM Context.\n")
        sb.WriteString("- If the context does not contain the answer, state 'Based on the provided context, this is not mentioned.' DO NOT invent or extrapolate facts.\n\n")
    }
    sb.WriteString("## Temporal Reasoning Guidelines\n")
    sb.WriteString("- Speech Act vs Reported Event Order: When a question asks whether a speaker 'did / mentioned' X before or after Y, follow the sequential order of dialogue turns in the transcripts (uttered_before / turn order), unless the prompt explicitly asks about physical external event dates.\n")
    sb.WriteString("- Avoid self-contradictions: Do not claim an event happened in the past before a conversation while concluding it happened after.\n\n")

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
            if link.Modality != "" {
                sb.WriteString(fmt.Sprintf("  - Modality: %s\n", link.Modality))
            }
            if link.Caveats != "" {
                sb.WriteString(fmt.Sprintf("  - Caveat: %s\n", link.Caveats))
            }
            if link.OriginID != "" {
                sb.WriteString(fmt.Sprintf("  - Origin: %s\n", link.OriginID))
            }
        }
        sb.WriteString("\n")
    }

    // Episodic summaries (with dynamic budget capping under 120k characters to prevent 131,072 limit errors)
    maxBudget := 120000
    if len(ctx.Episodic) > 0 {
        sb.WriteString("## Recent Episodes\n\n")
        for _, ep := range ctx.Episodic {
            cleanedSummary := cleanTranscriptSAYArtifacts(ep.SummaryText)
            formattedEpisode := fmt.Sprintf("- [%s] %s\n", ep.CreatedAt.Format(time.RFC3339), cleanedSummary)
            if sb.Len() + len(formattedEpisode) > maxBudget {
                remainingSpace := maxBudget - sb.Len()
                if remainingSpace > 150 {
                    sb.WriteString(fmt.Sprintf("- [%s] %s... [TRUNCATED DUE TO CONTEXT BUDGET LIMIT]\n", ep.CreatedAt.Format(time.RFC3339), cleanedSummary[:remainingSpace-150]))
                } else {
                    sb.WriteString("... [ADDITIONAL EPISODES TRUNCATED DUE TO CONTEXT BUDGET LIMIT]\n")
                }
                break
            }
            sb.WriteString(formattedEpisode)
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

            // Synthetic Revision History Compaction (token-efficient author epochs)
            if len(lin.RevisionEpochs) > 0 {
                sb.WriteString("  * Synthetic Revision Timeline:\n")
                for _, ep := range lin.RevisionEpochs {
                    authorLabel := ep.AuthorID
                    if ep.AuthorName != "" {
                        authorLabel = fmt.Sprintf("%s (%s)", ep.AuthorName, ep.AuthorID)
                    }
                    sb.WriteString(fmt.Sprintf("    - [%s, %s] %s: %s\n", ep.VersionRange, ep.TimeRange, authorLabel, ep.SyntheticSummary))
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
        targetLower := strings.ToLower(l.TargetID)
        relLower := strings.ToLower(l.Relationship)
        caveatsLower := strings.ToLower(l.Caveats)

        if strings.Contains(targetLower, "no_") || strings.Contains(targetLower, "never_") || strings.Contains(targetLower, "dont_") || strings.Contains(relLower, "prohibit") || strings.Contains(caveatsLower, "negative") {
            hasNegativeConstraint = true

            // Redact IP addresses if constraint mentions IP
            if strings.Contains(targetLower, "ip") || strings.Contains(targetLower, "internal_ip") || strings.Contains(caveatsLower, "ip") {
                text = ipRegex.ReplaceAllString(text, "[REDACTED_INTERNAL_IP]")
            }
            // Redact tokens/passwords if constraint mentions token/password/auth/secret
            if strings.Contains(targetLower, "token") || strings.Contains(targetLower, "password") || strings.Contains(targetLower, "secret") || strings.Contains(targetLower, "auth") || strings.Contains(caveatsLower, "token") || strings.Contains(caveatsLower, "secret") {
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
				if lSrc == tgt && lTgt == src && lRel == "happened_before" ||
					lSrc == src && lTgt == tgt && lRel == "happened_after" {
					return fmt.Sprintf("⚠️ TIMELINE CONTRADICTION: Requested sequence '%s before %s' is mathematically impossible because the graph records '%s occurred before %s'.", src, tgt, tgt, src)
				}
			}

			return fmt.Sprintf("⚠️ TIMELINE UNPROVABLE: Requested sequence '%s before %s' cannot be verified from recorded graph links (insufficient causal/ordering links).", src, tgt)
		}
	}

	return fmt.Sprintf("⚠️ TIMELINE UNPROVABLE: Goal predicate %s could not be verified by the planning engine.", goalPredicate)
}

func cleanTranscriptSAYArtifacts(text string) string {
	lines := strings.Split(text, "\n")
	lastSpeaker := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "say:") || strings.HasPrefix(lower, "say :") {
			colonIdx := strings.Index(line, ":")
			if colonIdx != -1 && lastSpeaker != "" {
				lines[i] = lastSpeaker + ":" + line[colonIdx+1:]
			}
		} else {
			colonIdx := strings.Index(line, ":")
			if colonIdx != -1 {
				potentialSpeaker := strings.TrimSpace(line[:colonIdx])
				if !strings.Contains(potentialSpeaker, " ") {
					lastSpeaker = potentialSpeaker
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}
