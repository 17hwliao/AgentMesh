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
	sql := flag.String("sql", "", "SQL text to discuss; it is never executed by this CLI")
	flag.Parse()
	if strings.TrimSpace(*sql) == "" {
		log.Fatal("--sql is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	messages := []provider.Message{
		{Role: "system", Content: "Describe SQL diagnostic considerations. Do not execute the SQL."},
		{Role: "user", Content: *sql},
	}
	if err := gatewayclient.Stream(ctx, *endpoint, *model, messages, os.Stdout); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintln(os.Stdout)
}
