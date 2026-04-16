package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clone45/tasks127/internal/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// runMCP is the entry point for `tasks127 mcp [flags]`.
//
// By default it speaks MCP over stdio, which is what MCP clients like
// OpenClaw, Claude Desktop, and Claude Code spawn the server as. Pass
// --http ADDR to speak MCP over Streamable HTTP instead; this is useful
// when several clients share one long-running server, or for debugging
// with curl-based tooling.
//
// The MCP server is a thin translation layer over the tasks127 REST API.
// It does not open the database. Configure it with TASKS127_URL and
// TASKS127_API_KEY environment variables.
func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	httpAddr := fs.String("http", "", "serve MCP over Streamable HTTP on this address (e.g. 127.0.0.1:8090); omit for stdio")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: tasks127 mcp [--http ADDR]\n\n")
		fmt.Fprintf(os.Stderr, "Serves tasks127 tools over the Model Context Protocol.\n\n")
		fmt.Fprintf(os.Stderr, "Required environment:\n")
		fmt.Fprintf(os.Stderr, "  TASKS127_URL      base URL of the tasks127 REST server (default http://127.0.0.1:8080)\n")
		fmt.Fprintf(os.Stderr, "  TASKS127_API_KEY  bearer token the MCP server will use to call the REST API\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	baseURL := os.Getenv("TASKS127_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	apiKey := os.Getenv("TASKS127_API_KEY")
	if apiKey == "" {
		// stdio-mode clients read/write JSON-RPC on stdout, so we log errors
		// to stderr rather than mixing them into the protocol stream.
		fmt.Fprintln(os.Stderr, "tasks127 mcp: TASKS127_API_KEY is required")
		os.Exit(2)
	}

	client := mcp.NewClient(baseURL, apiKey)
	server := mcp.NewServer(client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Translate SIGINT/SIGTERM into context cancellation so transports shut down cleanly.
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		cancel()
	}()

	if *httpAddr != "" {
		runMCPHTTPServer(ctx, server, *httpAddr)
		return
	}
	runMCPStdio(ctx, server)
}

func runMCPStdio(ctx context.Context, server *sdk.Server) {
	if err := server.Run(ctx, &sdk.StdioTransport{}); err != nil {
		// stdio transport: writing to stderr is the only safe way to report.
		fmt.Fprintf(os.Stderr, "tasks127 mcp (stdio): %v\n", err)
		os.Exit(1)
	}
}

func runMCPHTTPServer(ctx context.Context, server *sdk.Server, addr string) {
	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server },
		nil,
	)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	httpServer := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("tasks127 mcp (http) listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("mcp http: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("tasks127 mcp: shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("mcp http shutdown: %v", err)
	}
}
