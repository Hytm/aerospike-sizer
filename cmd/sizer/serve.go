package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Hytm/aerospike-sizer/internal/buildinfo"
	"github.com/Hytm/aerospike-sizer/internal/config"
	"github.com/Hytm/aerospike-sizer/internal/pricing"
	"github.com/Hytm/aerospike-sizer/internal/service"
	"github.com/Hytm/aerospike-sizer/internal/web"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	slot := fs.String("slot", "", "launchd instance name (a or b)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Load()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil)).With("pid", os.Getpid(), "slot", *slot)
	prices := &pricing.Book{} // without fetchers: reference prices only
	if !cfg.Offline {
		prices = pricing.NewBook(log)
	}
	srv := &web.Server{Log: log, Slot: *slot, Prices: prices}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ln, err := service.Listen(ctx, cfg.Addr, cfg.SharedPorts)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("listening", "addr", "http://"+ln.Addr().String(), "version", buildinfo.Version, "live_prices", !cfg.Offline)

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	log.Info("stopping: draining requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), service.DrainSeconds*time.Second)
	defer cancel()
	err = httpSrv.Shutdown(shutdownCtx)
	log.Info("stopped")
	return err
}
