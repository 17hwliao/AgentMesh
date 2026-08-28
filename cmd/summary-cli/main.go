package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"agentmesh/internal/gatewayclient"
	"agentmesh/internal/provider"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:18080", "local AgentMesh endpoint")
	model := flag.String("model", "mock-model", "requested model name")
	text := flag.String("text", "", "document text to summarize")
	flag.Parse()
	if strings.TrimSpace(*text) == "" {
		log.Fatal("--text is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	messages := []provider.Message{
		{Role: "system", Content: "Summarize the supplied document concisely."},
		{Role: "user", Content: *text},
	}
	if err := gatewayclient.Stream(ctx, *endpoint, os.Getenv("AGENTMESH_API_KEY"), *model, messages, os.Stdout); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintln(os.Stdout)
}
