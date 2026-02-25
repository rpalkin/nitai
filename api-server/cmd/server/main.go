package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	migrate "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"ai-reviewer/gen/api/v1/apiv1connect"
	apimigrations "ai-reviewer/api-server/migrations"
	"ai-reviewer/api-server/internal/config"
	"ai-reviewer/api-server/internal/crypto"
	"ai-reviewer/api-server/internal/db"
	"ai-reviewer/api-server/internal/handler"
	"ai-reviewer/api-server/internal/restate"
)

func main() {
	cfg := config.Load()

	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if cfg.EncryptionKey == "" {
		log.Fatal("ENCRYPTION_KEY is required")
	}
	if cfg.RestateIngressURL == "" {
		log.Fatal("RESTATE_INGRESS_URL is required")
	}
	if cfg.RestateAdminURL == "" {
		log.Fatal("RESTATE_ADMIN_URL is required")
	}

	encKey, err := crypto.DecodeKey(cfg.EncryptionKey)
	if err != nil {
		log.Fatalf("invalid ENCRYPTION_KEY: %v", err)
	}

	// Run migrations.
	migrationsFS, err := iofs.New(apimigrations.FS, ".")
	if err != nil {
		log.Fatalf("loading migrations: %v", err)
	}

	// golang-migrate pgx/v5 driver uses pgx5:// scheme.
	migrateURL := strings.Replace(cfg.DatabaseURL, "postgres://", "pgx5://", 1)
	m, err := migrate.NewWithSourceInstance("iofs", migrationsFS, migrateURL)
	if err != nil {
		log.Fatalf("creating migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatalf("running migrations: %v", err)
	}
	log.Println("migrations applied")

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

	restateClient := restate.New(cfg.RestateIngressURL, cfg.RestateAdminURL)

	mux := http.NewServeMux()

	providerHandler := handler.NewProviderHandler(pool, encKey)
	repoHandler := handler.NewRepoHandler(pool)
	reviewHandler := handler.NewReviewHandler(pool, restateClient)

	mux.Handle(apiv1connect.NewProviderServiceHandler(providerHandler, connect.WithRecover(recoverHandler)))
	mux.Handle(apiv1connect.NewRepoServiceHandler(repoHandler, connect.WithRecover(recoverHandler)))
	mux.Handle(apiv1connect.NewReviewServiceHandler(reviewHandler, connect.WithRecover(recoverHandler)))
	mux.Handle("/webhooks/", handler.NewWebhookHandler(&handler.PoolWebhookStore{Pool: pool}, restateClient))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}

	go func() {
		<-ctx.Done()
		log.Println("shutting down")
		if err := srv.Shutdown(context.Background()); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("api-server listening on %s", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func recoverHandler(ctx context.Context, spec connect.Spec, header http.Header, r any) error {
	log.Printf("panic in %s: %v", spec.Procedure, r)
	return connect.NewError(connect.CodeInternal, nil)
}
