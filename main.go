package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("open store (%s): %v", cfg.DBPath, err)
	}
	defer store.Close()
	log.Printf("db: ready (%s)", cfg.DBPath)

	ctx, cancel := signalContext()
	defer cancel()

	// Background: Proton sync loop
	sync := NewProtonSync(cfg, store)
	go func() {
		if err := sync.Start(ctx); err != nil && ctx.Err() == nil {
			log.Printf("proton sync stopped: %v", err)
		}
	}()

	// Background: Geocoding loop
	geo := NewGeocoder(store, cfg.NominatimUserAgent)
	go geo.Run(ctx)

	// HTTP server
	web, err := NewWeb(store)
	if err != nil {
		log.Fatalf("web: %v", err)
	}
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           web.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Printf("http: listening on :%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http: %v", err)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("shutting down")
		cancel()
	}()
	return ctx, cancel
}
