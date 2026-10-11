package main

import (
	"agentmesh/internal/configenv"
	"agentmesh/internal/reservation"
	"context"
	"flag"
	redis "github.com/redis/go-redis/v9"
	"log"
	"os"
	"time"
)

func main() {
	id := flag.String("tenant", "", "tenant to rebuild")
	offline := flag.Bool("offline", false, "confirm all API and usage processes are stopped")
	flag.Parse()
	if !*offline || *id == "" {
		log.Fatal("offline_confirmation_and_tenant_required")
	}
	if err := configenv.LoadSecretFiles(); err != nil {
		log.Fatal("secret_configuration_failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, db, err := reservation.OpenMySQLRepository(os.Getenv("AGENTMESH_QUOTA_MYSQL_DSN"), nil)
	if err != nil {
		log.Fatal("database_configuration_failed")
	}
	defer db.Close()
	options, err := redis.ParseURL(os.Getenv("AGENTMESH_QUOTA_REDIS_URL"))
	if err != nil {
		log.Fatal("redis_configuration_failed")
	}
	client := redis.NewClient(options)
	defer client.Close()
	if err = (&reservation.Runtime{DB: db, Redis: client}).RebuildWallet(ctx, *id); err != nil {
		log.Fatal("quota_rebuild_failed")
	}
	log.Print("quota_rebuild_complete")
}
