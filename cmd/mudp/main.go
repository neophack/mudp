package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mudp/internal/config"
	"mudp/internal/server"
	"mudp/internal/store"
)

func main() {
	cfg := config.Load()

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(cfg.AdminUser, cfg.AdminPassword); err != nil {
		log.Fatalf("migrate database: %v", err)
	}

	app, err := server.New(cfg, db)
	if err != nil {
		log.Fatalf("start mudp: %v", err)
	}

	backgroundCtx, cancelBackground := context.WithCancel(context.Background())
	stopJobs := app.StartBackgroundJobs(backgroundCtx)
	defer stopJobs()
	defer cancelBackground()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}

	go func() {
		log.Printf("mudp listening on http://%s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
	if err := app.Close(); err != nil {
		log.Printf("close app: %v", err)
	}
}
