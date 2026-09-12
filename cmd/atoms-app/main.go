package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/local/atoms/internal/platform"
)

func main() {
	cfg, err := platform.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	app, err := platform.NewApp(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: app.Router(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("atoms-app listening on http://localhost:%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Println(err)
		os.Exit(1)
	}
}
