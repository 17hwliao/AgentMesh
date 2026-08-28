package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"agentmesh/internal/gateway"
	"agentmesh/internal/provider"
	"agentmesh/internal/router"
)

func main() {
	flags := flag.NewFlagSet("api", flag.ExitOnError)
	address := flags.String("addr", "127.0.0.1:18080", "local listen address (must be 127.0.0.1:PORT)")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [--addr 127.0.0.1:PORT]\n", os.Args[0])
		flags.PrintDefaults()
	}
	_ = flags.Parse(os.Args[1:])
	if err := gateway.ValidateListenAddress(*address); err != nil {
		log.Fatal(err)
	}

	primary := provider.NewMock(provider.MockConfig{Name: "mock-primary", FailBeforeFirst: true, FailAfterChunks: -1})
	fallback := provider.NewMock(provider.MockConfig{Name: "mock-fallback", Chunks: []string{"mock response"}, FailAfterChunks: -1})
	server := gateway.New(router.New(primary, fallback))
	log.Printf("AgentMesh mock gateway listening on http://%s", *address)
	log.Fatal(http.ListenAndServe(*address, server.Handler()))
}
