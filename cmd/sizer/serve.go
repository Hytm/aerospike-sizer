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
	srv := &web.Server{Log: log, Slot: *slot, Prices: prices, Addr: cfg.Addr}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// Once the first signal starts the drain, a second one ends the process at once.
	go func() {
		<-ctx.Done()
		stop()
	}()
	ln, err := service.Listen(ctx, cfg.Addr, cfg.SharedPorts)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("listening", "addr", "http://"+ln.Addr().String(), "version", buildinfo.Version, "live_prices", !cfg.Offline)
	err = service.Serve(ctx, httpSrv, ln, service.DrainSeconds*time.Second, func() { log.Info("stopping: finishing the requests in flight") })
	log.Info("stopped")
	return err
}
