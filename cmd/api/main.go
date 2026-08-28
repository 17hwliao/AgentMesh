package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"agentmesh/internal/gateway"
	"agentmesh/internal/router"
	"agentmesh/internal/runtime"
)

func main() {
	flags := flag.NewFlagSet("api", flag.ExitOnError)
	address := flags.String("addr", "127.0.0.1:18080", "local listen address (must be 127.0.0.1:PORT)")
	providerOrder := flags.String("providers", "mock", "provider route: mock or comma-separated ark,ollama")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [--addr 127.0.0.1:PORT]\n", os.Args[0])
		flags.PrintDefaults()
	}
	_ = flags.Parse(os.Args[1:])
	if err := gateway.ValidateListenAddress(*address); err != nil {
		log.Fatal(err)
	}

	providers, err := runtime.Build(*providerOrder, os.Getenv)
	if err != nil {
		if code, ok := runtime.IsConfigurationError(err); ok {
			log.Fatal(code)
		}
		log.Fatal("provider_configuration_invalid")
	}
	server := gateway.NewWithHealth(router.New(providers...), providers...)
	log.Printf("AgentMesh gateway listening on http://%s", *address)
	log.Fatal(http.ListenAndServe(*address, server.Handler()))
}
