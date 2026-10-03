// Command api runs the 1x.n HTTP API.
//
//	DATABASE_URL  connection string for the app role (not the owner)
//	HTTP_ADDR     listen address, default :8080
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"1xn/core/internal/app"
	"1xn/core/internal/platform/db"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("api stopped", "err", err)
		if h := hint(err); h != "" {
			fmt.Fprintln(os.Stderr, "\n"+h)
		}
		os.Exit(1)
	}
}

// hint says what to do about the two ways a local start usually fails.
func hint(err error) string {
	if errors.Is(err, syscall.EADDRINUSE) {
		addr := os.Getenv("HTTP_ADDR")
		if addr == "" {
			addr = ":8080"
		}
		return fmt.Sprintf("Something already listens on %s: most likely the API is already running in another terminal.\n"+
			"Use that one, or stop it there with Ctrl-C. To see who has the port: lsof -nP -iTCP%s -sTCP:LISTEN", addr, addr)
	}
	return db.Hint(err, os.Getenv("DATABASE_URL"))
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is not set (see .env.example)")
	}
	d, err := db.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer d.Close()

	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{
		Addr:    addr,
		Handler: app.New(d),
		// Timeouts stop slow or stalled clients from holding connections open.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Take the port first, so "listening" is only ever said when it is true.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("api listening", "addr", addr, "health", "http://localhost"+addr[strings.LastIndex(addr, ":"):]+"/healthz")
		errc <- srv.Serve(ln)
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// Graceful shutdown: stop accepting, finish in-flight requests (a bill
	// half-way through its transaction completes instead of being cut).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	slog.Info("api shutting down")
	return srv.Shutdown(shutdownCtx)
}
