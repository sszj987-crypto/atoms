package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/local/atoms/internal/platform"
)

func main() {
	cfg, err := platform.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app, err := platform.NewApp(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("atoms-app listening on http://localhost:%s", cfg.Port)
	servers := []*http.Server{srv}
	for _, port := range app.PreviewPorts() {
		servers = append(servers, &http.Server{Addr: ":" + port, Handler: app.PreviewHandler(port), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second})
	}
	// Bind every configured preview port before accepting API requests, so the
	// frontend never receives a URL for an unavailable gateway listener.
	var listeners []net.Listener
	for _, server := range servers {
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			log.Fatalf("HTTP listen %s: %v", server.Addr, err)
		}
		listeners = append(listeners, listener)
	}
	serverErrors := make(chan error, len(servers))
	for i, server := range servers {
		go func() { serverErrors <- server.Serve(listeners[i]) }()
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		if err := app.Shutdown(shutdownCtx); err != nil {
			log.Printf("task shutdown cleanup failed: %v", err)
		}
		for _, server := range servers {
			if err := server.Shutdown(shutdownCtx); err != nil {
				log.Printf("HTTP shutdown failed: %v", err)
			}
		}
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}
}
