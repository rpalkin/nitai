package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/server"

	"ai-reviewer/go-services/internal/config"
	"ai-reviewer/go-services/internal/crypto"
	"ai-reviewer/go-services/internal/db"
	"ai-reviewer/go-services/internal/difffetcher"
	"ai-reviewer/go-services/internal/postreview"
	"ai-reviewer/go-services/internal/prreview"
	"ai-reviewer/go-services/internal/reposyncer"
)

func main() {
	cfg := config.Load()

	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if cfg.EncryptionKey == "" {
		log.Fatal("ENCRYPTION_KEY is required")
	}

	encKey, err := crypto.DecodeKey(cfg.EncryptionKey)
	if err != nil {
		log.Fatalf("invalid ENCRYPTION_KEY: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("creating DB pool: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("pinging DB: %v", err)
	}
	log.Println("connected to database")

	diffFetcher := difffetcher.New(pool, encKey)
	postReviewSvc := postreview.New(pool, encKey)
	prReviewSvc := prreview.New(pool)
	repoSyncerSvc := reposyncer.New(pool, encKey)

	log.Printf("starting worker on %s", cfg.WorkerAddr)
	if err := server.NewRestate().
		Bind(restate.Reflect(diffFetcher)).
		Bind(restate.Reflect(postReviewSvc)).
		Bind(restate.Reflect(prReviewSvc)).
		Bind(restate.Reflect(repoSyncerSvc)).
		Start(ctx, cfg.WorkerAddr); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
