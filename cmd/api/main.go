package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"agentmesh/internal/auth"
	"agentmesh/internal/gateway"
	"agentmesh/internal/observability"
	"agentmesh/internal/reservation"
	"agentmesh/internal/runtime"
	"agentmesh/internal/tenant"
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

	logicalProviders, err := runtime.Selection(*providerOrder)
	if err != nil {
		if code, ok := runtime.IsConfigurationError(err); ok {
			log.Fatal(code)
		}
		log.Fatal("provider_selection_invalid")
	}
	store, err := auth.Bootstrap(os.Getenv)
	if err != nil {
		if code, ok := auth.IsConfigurationError(err); ok {
			log.Fatal(code)
		}
		log.Fatal("auth_configuration_invalid")
	}
	providers, err := runtime.Build(*providerOrder, os.Getenv)
	if err != nil {
		if code, ok := runtime.IsConfigurationError(err); ok {
			log.Fatal(code)
		}
		log.Fatal("provider_configuration_invalid")
	}
	resolver, err := tenant.NewResolver(store, logicalProviders, providers)
	if err != nil {
		log.Fatal("tenant_route_configuration_invalid")
	}
	reservationGate, cleanupReservation, err := reservation.OpenConfiguredCoordinator(os.Getenv)
	if err != nil {
		if code := reservation.Code(err); code != "" {
			log.Fatal(code)
		}
		log.Fatal("quota_configuration_invalid")
	}
	defer cleanupReservation()
	server := gateway.NewWithTenantRoutingAndRecorderAndReservations(resolver, observability.NewRecorder(observability.DefaultCapacity, nil, nil), reservationGate)
	log.Printf("AgentMesh gateway listening on http://%s", *address)
	if err := http.ListenAndServe(*address, server.AuthenticatedHandler(func(next http.Handler) http.Handler {
		return auth.Authenticate(store, next)
	})); err != nil {
		log.Print(err)
	}
}
