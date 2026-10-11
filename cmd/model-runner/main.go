package main

import (
	"agentmesh/internal/configenv"
	"agentmesh/internal/modelrpc"
	"agentmesh/internal/runtime"
	"agentmesh/internal/securegrpc"
	"agentmesh/internal/telemetry"
	"agentmesh/internal/tenant"
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if configenv.LoadSecretFiles() != nil {
		log.Fatal("secret_configuration_failed")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	traces, e := telemetry.New(ctx, "agentmesh-model-runner", os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if e != nil {
		log.Fatal("telemetry_configuration_invalid")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = traces.Close(cleanup)
	}()
	lookup := func(name string) string {
		if name == "AGENTMESH_RUNNER_GRPC_TARGET" {
			return ""
		}
		return os.Getenv(name)
	}
	providers, e := runtime.Build(os.Getenv("AGENTMESH_RUNNER_PROVIDERS"), lookup)
	if e != nil {
		log.Fatal("runner_providers_invalid")
	}
	store, db, e := tenant.OpenMySQLStore(os.Getenv("AGENTMESH_AUTH_MYSQL_DSN"))
	if e != nil {
		log.Fatal("identity_configuration_invalid")
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	start, cancel := context.WithTimeout(ctx, 5*time.Second)
	e = db.PingContext(start)
	cancel()
	if e != nil {
		log.Fatal("identity_unavailable")
	}
	server, e := securegrpc.Server(securegrpc.Env(os.Getenv, "AGENTMESH_GRPC"))
	if e != nil {
		log.Fatal("grpc_configuration_invalid")
	}
	modelrpc.RegisterModelRunnerServer(server, modelrpc.NewServer(providers, func(ctx context.Context, t, m, p string) bool {
		route, ok := store.Route(ctx, t, m)
		if !ok {
			return false
		}
		for _, name := range route {
			if p == name {
				return true
			}
		}
		return false
	}))
	address := os.Getenv("AGENTMESH_RUNNER_LISTEN")
	if address == "" {
		address = "127.0.0.1:50051"
	}
	listener, e := net.Listen("tcp", address)
	if e != nil {
		log.Fatal("listen_failed")
	}
	go func() {
		<-ctx.Done()
		done := make(chan struct{})
		go func() { server.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			server.Stop()
		}
	}()
	if e = server.Serve(listener); e != nil && ctx.Err() == nil {
		log.Fatal("grpc_serve_failed")
	}
}
