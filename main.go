package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"looking-glass/internal/bgp"
	"looking-glass/internal/geoip"
	"looking-glass/internal/handler"
	"looking-glass/internal/ratelimit"
	"looking-glass/internal/report"
)

//go:embed web
var webFS embed.FS

func main() {
	bgpPath := envOr("BGP_DATA_PATH", "/var/lib/looking-glass/bgp.json")
	listenAddr := envOr("LISTEN_ADDR", "127.0.0.1:8082")

	store := bgp.NewStore(bgpPath)
	store.Start()

	var geo *geoip.DB
geoPaths := []string{
	envOr("GEOIP_PATH", "/var/lib/looking-glass/ipinfo_lite.csv.gz"),
}
if p2 := os.Getenv("GEOIP_PATH2"); p2 != "" {
	geoPaths = append(geoPaths, p2)
}

if g, err := geoip.Open(geoPaths...); err != nil {
	log.Printf("[geoip] skipped: %v", err)
} else {
	geo = g
}

	rl := ratelimit.New(20, 5)
	promoteRL := ratelimit.NewPerHour(10, 3)

	reportsDir := envOr("REPORTS_DIR", "/var/lib/looking-glass/reports")
	ephemeral := report.NewEphemeralCache()
	reports, err := report.NewStore(reportsDir)
	if err != nil {
		log.Printf("[report] permanent links disabled: %v", err)
	}

	h := handler.New(store, geo, rl, nil, ephemeral, reports, promoteRL)

	fsys, _ := fs.Sub(webFS, "web")

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(fsys)))
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
	mux.HandleFunc("GET /api/ip-info", h.IPInfo)
	mux.HandleFunc("POST /api/report/promote", h.Promote)
	mux.HandleFunc("GET /api/report", h.ReportRead)

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
