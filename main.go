package main

import (
	"context"
	_ "embed"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"looking-glass/internal/bgp"
	"looking-glass/internal/handler"
	"looking-glass/internal/ratelimit"
)

//go:embed web/index.html
var indexHTML []byte

func main() {
	bgpPath := envOr("BGP_DATA_PATH", "/var/lib/looking-glass/bgp.json")
	listenAddr := envOr("LISTEN_ADDR", "127.0.0.1:8082")

	store := bgp.NewStore(bgpPath)
	store.Start()

	rl := ratelimit.New(20, 5)

	h := handler.New(store, rl, indexHTML)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.Index)
	mux.HandleFunc("GET /api/myip", h.MyIP)
	mux.HandleFunc("GET /api/info", h.Info)
	mux.HandleFunc("GET /api/ping", h.Ping)
	mux.HandleFunc("GET /api/traceroute", h.Traceroute)
	mux.HandleFunc("GET /api/bgp", h.BGP)
	mux.HandleFunc("GET /api/dig", h.Dig)
	mux.HandleFunc("GET /api/ssl", h.SSLCheck)
	mux.HandleFunc("GET /api/nodes", h.Nodes)
	mux.HandleFunc("GET /api/proxy", h.Proxy)
	mux.HandleFunc("GET /api/portcheck", h.PortCheck)
	mux.HandleFunc("GET /api/ping-all", h.PingAll)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 150 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[lg] listening on %s | bgp data: %s", listenAddr, bgpPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[lg] fatal: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[lg] shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[lg] shutdown error: %v", err)
	}
	log.Println("[lg] stopped")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
