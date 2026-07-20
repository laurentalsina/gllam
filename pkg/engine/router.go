package engine

import (
    "context"
    "fmt"
    "strings"

    "github.com/laurentalsina/gllam/pkg/memory"
)

// RouteAndAssemble classifies the user prompt and assembles a structured context
func (e *GllamEngine) RouteAndAssemble(ctx context.Context, userPrompt string, entities []string) (*memory.CompiledContext, error) {
    ctxResult := &memory.CompiledContext{}

    // Heuristic: detect action keywords for procedural retrieval
    actionKeywords := []string{"how", "deploy", "fix", "build", "configure", "setup", "install", "create", "implement"}
    isProcedural := false
    for _, kw := range actionKeywords {
        if strings.Contains(strings.ToLower(userPrompt), kw) {
            isProcedural = true
            break
        }
    }

    // Retrieve procedural knowledge if action keywords detected
    if isProcedural {
        procedures, err := e.GetTopProcedures(ctx, 5)
        if err != nil {
            return nil, fmt.Errorf("failed to retrieve procedures: %w", err)
        }
        ctxResult.Procedural = procedures
    }

    // Retrieve semantic links for requested entities
    if len(entities) > 0 {
        var links []memory.SemanticLink
        for _, entity := range entities {
            // Query active links where entity is source or target
            query := `
                SELECT source_id, target_id, relationship, caveats, valid_from, valid_until, updated_at
                FROM semantic_links
                WHERE valid_until IS NULL AND (source_id = ? OR target_id = ?)`

            rows, err := e.db.QueryContext(ctx, query, entity, entity)
            if err != nil {
                return nil, fmt.Errorf("failed to query links for entity %s: %w", entity, err)
            }

            for rows.Next() {
                var link memory.SemanticLink
                if err := rows.Scan(&link.SourceID, &link.TargetID, &link.Relationship, &link.Caveats,
                    &link.ValidFrom, &link.ValidUntil, &link.UpdatedAt); err != nil {
                    rows.Close()
                    return nil, fmt.Errorf("failed to scan link: %w", err)
                }
                links = append(links, link)
            }
            rows.Close()
        }
        ctxResult.Semantic = links
    }

    // Retrieve unresolved contradictions
    contradictions, err := e.GetUnresolvedContradictions(ctx)
    if err != nil {
        return nil, fmt.Errorf("failed to get contradictions: %w", err)
    }
    ctxResult.Contradictions = contradictions

    // Build grilling prompt from contradictions
    if len(contradictions) > 0 {
        ctxResult.HasConflicts = true
        var prompts []string
        for _, c := range contradictions {
            prompts = append(prompts, fmt.Sprintf(
                "⚠️ Contradiction %s: (%s) -[%s]-> (%s) vs (%s) -[%s]-> (%s). Please clarify.",
                c.ID, c.Link1SourceID, c.Link1Relationship, c.Link1TargetID,
                c.Link2SourceID, c.Link2Relationship, c.Link2TargetID))
        }
        ctxResult.GrillingPrompt = strings.Join(prompts, "\n")
    }

    // Always append recent episodic summaries for continuity
    episodes, err := e.GetRecentEpisodes(ctx, 3)
    if err != nil {
        return nil, fmt.Errorf("failed to get recent episodes: %w", err)
    }
    ctxResult.Episodic = episodes

    return ctxResult, nil
}

// FormatSystemPrompt formats the compiled context into a Markdown block for LLM consumption
func FormatSystemPrompt(ctx *memory.CompiledContext) string {
    var sb strings.Builder

    sb.WriteString("# GLLAM Context\n\n")

    // Grilling prompt first if conflicts exist
    if ctx.HasConflicts && ctx.GrillingPrompt != "" {
        sb.WriteString(ctx.GrillingPrompt + "\n\n")
    }

    // Contradictions detail
    if len(ctx.Contradictions) > 0 {
        sb.WriteString("## Active Contradictions\n\n")
        for _, c := range ctx.Contradictions {
            sb.WriteString(fmt.Sprintf("### %s\n", c.ID))
            sb.WriteString(fmt.Sprintf("- Link 1: (%s) -[%s]-> (%s)\n", c.Link1SourceID, c.Link1Relationship, c.Link1TargetID))
            sb.WriteString(fmt.Sprintf("- Link 2: (%s) -[%s]-> (%s)\n", c.Link2SourceID, c.Link2Relationship, c.Link2TargetID))
            sb.WriteString(fmt.Sprintf("- Detected: %s\n", formatTimestamp(c.DetectedAt)))
            if c.ResolutionNotes != "" {
                sb.WriteString(fmt.Sprintf("- Resolution: %s\n", c.ResolutionNotes))
            }
            sb.WriteString("\n")
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

    // Semantic links
    if len(ctx.Semantic) > 0 {
        sb.WriteString("## Semantic Graph\n\n")
        for _, link := range ctx.Semantic {
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

    return sb.String()
}

// formatTimestamp converts a Unix timestamp to a readable format
func formatTimestamp(ts int64) string {
    return fmt.Sprintf("%d", ts)
}
