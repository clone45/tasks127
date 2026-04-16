package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clone45/tasks127/internal/auth"
	"github.com/clone45/tasks127/internal/config"
	"github.com/clone45/tasks127/internal/db"
	"github.com/clone45/tasks127/internal/server"
	"github.com/oklog/ulid/v2"
)

func main() {
	cfg := config.Load()

	database, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()

	if cfg.MigrateOnStart {
		if err := db.Migrate(database); err != nil {
			log.Fatalf("migrate: %v", err)
		}
	}

	if err := bootstrapAdminKey(database); err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	srv := server.New(cfg, database)
	srv.StartWebhookWorker()

	httpServer := &http.Server{
		Addr:    cfg.Bind,
		Handler: srv.Handler(),
	}

	go func() {
		log.Printf("tasks127 listening on %s", cfg.Bind)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Stop accepting new requests first, then drain webhook worker.
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	if err := srv.ShutdownWebhookWorker(ctx); err != nil {
		log.Printf("webhook worker shutdown: %v", err)
	}
	log.Println("bye")
}

func bootstrapAdminKey(database *sql.DB) error {
	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM api_keys WHERE tier = 'admin' AND deleted_at IS NULL`,
	).Scan(&count); err != nil {
		return fmt.Errorf("check admin keys: %w", err)
	}
	if count > 0 {
		return nil
	}

	plaintext, hash, prefix, err := auth.GenerateKey()
	if err != nil {
		return err
	}

	id := ulid.Make().String()
	if _, err := database.Exec(
		`INSERT INTO api_keys (id, key_hash, key_prefix, tier, name) VALUES (?, ?, ?, 'admin', 'bootstrap')`,
		id, hash, prefix,
	); err != nil {
		return fmt.Errorf("insert admin key: %w", err)
	}

	fmt.Println()
	fmt.Println("=== ADMIN API KEY (shown once, save it now) ===")
	fmt.Println(plaintext)
	fmt.Println("================================================")
	fmt.Println()

	return nil
}
