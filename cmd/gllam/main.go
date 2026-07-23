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
    recall := flag.String("recall", "", "Recall context for LLM (natural language query)")
    entity := flag.String("entity", "", "Entity to query (comma-separated)")
    dbPath := flag.String("db", "./gllam_data.db", "Path to SQLite database")
    embeddingsServer := flag.String("embeddings-server", "", "Embeddings server URL (e.g., http://localhost:8080)")
    textServer := flag.String("text-server", "", "Text-to-text server URL")
    configPath := flag.String("config", "", "Path to YAML config file")
    seed := flag.Bool("seed", false, "Seed sample data and exit")
    flag.Parse()

    ctx := context.Background()

    cfg := LoadConfig(*configPath)
    if *embeddingsServer != "" {
        cfg.EmbeddingEndpoint = *embeddingsServer
    }
    if *textServer != "" {
        cfg.TextEndpoint = *textServer
    }

    // Create embedder if embeddings server is provided
    var embedder engine.Embedder
    if cfg.EmbeddingEndpoint != "" {
        embedder = engine.NewLlamaEmbedder(cfg.EmbeddingEndpoint)
    }

    // Initialize engine
    gllam, err := engine.NewGllamEngine(*dbPath, embedder)
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

    var llmClient *engine.LLMClient
    if cfg.TextEndpoint != "" {
        llmClient = engine.NewLLMClient(cfg.TextEndpoint)
    }

    // If -recall flag is set, process and exit
    if *recall != "" {
        var entities []string
        if *entity != "" {
            entities = strings.Split(*entity, ",")
            for i := range entities {
                entities[i] = strings.TrimSpace(entities[i])
            }
        }

        compiled, err := gllam.RouteAndAssemble(ctx, *recall, entities)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Failed to route and assemble: %v\n", err)
            os.Exit(1)
        }

        prompt := engine.FormatSystemPrompt(compiled)
        if llmClient != nil {
            fmt.Println("--- Retrieved Context ---")
            fmt.Println(prompt)
            fmt.Println("\n--- Waiting for LLM ---")
            answer, err := llmClient.Generate(ctx, prompt, *recall)
            if err != nil {
                fmt.Fprintf(os.Stderr, "LLM error: %v\n", err)
                os.Exit(1)
            }
            fmt.Printf("\nAnswer:\n%s\n", answer)
        } else {
            fmt.Print(prompt)
        }
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
        if llmClient != nil {
            fmt.Println("--- Waiting for LLM ---")
            answer, err := llmClient.Generate(ctx, prompt, input)
            if err != nil {
                fmt.Fprintf(os.Stderr, "LLM error: %v\n", err)
            } else {
                fmt.Printf("\n%s\n", answer)
            }
        } else {
            fmt.Print(prompt)
        }
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
