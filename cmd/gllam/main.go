package main

import (
    "bufio"
    "context"
    "flag"
    "fmt"
    "os"
    "strings"
    "time"

    "github.com/laurentalsina/gllam/pkg/engine"
    "github.com/laurentalsina/gllam/pkg/memory"
)

func main() {
    query := flag.String("query", "", "Query to process")
    entity := flag.String("entity", "", "Entity to query (comma-separated)")
    dbPath := flag.String("db", "./gllam_data.db", "Path to SQLite database")
    seed := flag.Bool("seed", false, "Seed sample data and exit")
    flag.Parse()

    ctx := context.Background()

    // Initialize engine
    gllam, err := engine.NewGllamEngine(*dbPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
        os.Exit(1)
    }
    defer gllam.Close()

    // Initialize schema
    if err := gllam.InitSchema(); err != nil {
        fmt.Fprintf(os.Stderr, "Failed to initialize schema: %v\n", err)
        os.Exit(1)
    }

    // Seed sample data if requested
    if *seed {
        seedSampleData(ctx, gllam)
        fmt.Println("Sample data seeded. Exiting.")
        return
    }

    // If -query flag is set, process and exit
    if *query != "" {
        var entities []string
        if *entity != "" {
            entities = strings.Split(*entity, ",")
            for i := range entities {
                entities[i] = strings.TrimSpace(entities[i])
            }
        }

        compiled, err := gllam.RouteAndAssemble(ctx, *query, entities)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Failed to route and assemble: %v\n", err)
            os.Exit(1)
        }

        prompt := engine.FormatSystemPrompt(compiled)
        fmt.Print(prompt)
        return
    }

    // Interactive REPL mode
    fmt.Println("GLLAM Interactive Mode. Type 'quit' to exit.")
    scanner := bufio.NewScanner(os.Stdin)
    for {
        fmt.Print("> ")
        if !scanner.Scan() {
            break
        }
        input := strings.TrimSpace(scanner.Text())
        if input == "quit" || input == "exit" {
            break
        }
        if input == "" {
            continue
        }

        compiled, err := gllam.RouteAndAssemble(ctx, input, nil)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Error: %v\n", err)
            continue
        }

        prompt := engine.FormatSystemPrompt(compiled)
        fmt.Print(prompt)
    }
}

func seedSampleData(ctx context.Context, gllam *engine.GllamEngine) {
    // Seed procedural knowledge: Caddy deployment
    caddyProcedure := memory.ProceduralKnowledge{
        ID:                "caddy-tailscale-deploy",
        TaskType:          "deploy_caddy_reverse_proxy",
        Instructions:      "# Deploy Caddy as Tailscale Reverse Proxy\n\n1. Install Caddy: `sudo apt install caddy`\n2. Configure Caddyfile with Tailscale hostname\n3. Enable and start Caddy service\n4. Verify HTTPS is working",
        UserFeedbackRules: "Use Tailscale FQDN, not public IP. Enable ACME DNS challenge with Tailscale DNS provider.",
        TimesApplied:      3,
        IsHighlyHelpful:   true,
        Version:           1,
        UpdatedAt:         time.Now().Unix(),
    }
    gllam.UpsertProceduralKnowledge(ctx, caddyProcedure)

    // Seed semantic graph: Caddy binds to Tailscale IP
    caddyNode := memory.SemanticNode{ID: "caddy", Name: "Caddy", Type: "service"}
    gllam.UpsertNode(ctx, caddyNode)

    tailscaleNode := memory.SemanticNode{ID: "tailscale", Name: "Tailscale", Type: "network"}
    gllam.UpsertNode(ctx, tailscaleNode)

    bindLink := memory.SemanticLink{
        SourceID:     "caddy",
        TargetID:     "tailscale",
        Relationship: "binds_to",
        Caveats:      "Must use Tailscale FQDN (e.g., caddy.tailnet.ts.net)",
        ValidFrom:    time.Now().Unix(),
        UpdatedAt:    time.Now().Unix(),
    }
    gllam.AddEdge(ctx, bindLink)

    // Seed episodic summary
    summary := memory.EpisodicSummary{
        ID:          "session-001",
        SessionID:   "2024-07-20-morning",
        SummaryText: "Deployed Caddy reverse proxy with Tailscale integration for secure internal access",
        CreatedAt:   time.Now().Unix(),
    }
    gllam.SaveEpisodicSummary(ctx, summary)
}
