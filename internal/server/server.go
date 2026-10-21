// Package server wires Keera Chat together and runs it until it is told to
// stop.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bespinian/keera-chat/internal/chat"
	"github.com/bespinian/keera-chat/internal/config"
	"github.com/bespinian/keera-chat/internal/gateway"
	"github.com/bespinian/keera-chat/internal/secret"
	"github.com/bespinian/keera-chat/internal/version"
)

// Run starts Keera Chat and blocks until SIGINT or SIGTERM.
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)

	box, err := secret.New(cfg.SecretKey)
	if err != nil {
		return err
	}
	gw := gateway.New(cfg.GatewayInternalURL)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Not fatal: chat may well start before the gateway does.
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	if _, err := gw.AuthConfig(check); err != nil {
		log.Warn("Keera Gateway does not answer yet; sign-ins will fail until it does",
			"gateway", cfg.GatewayInternalURL, "error", err)
	}
	cancel()

	app := chat.New(chat.Options{
		PublicURL:     cfg.PublicURL,
		GatewayURL:    cfg.GatewayURL,
		SecureCookies: cfg.SecureCookies,
		MaxBodyBytes:  cfg.MaxBodyBytes,
	}, gw, box, log)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Generous enough for a message with images on a slow line.
		ReadTimeout: 2 * time.Minute,
		// For the small answers. A streamed reply lifts it for itself.
		WriteTimeout: time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("Keera Chat is listening", "addr", cfg.Addr, "public_url", cfg.PublicURL,
			"gateway", cfg.GatewayURL, "version", version.String())
		if !strings.HasPrefix(cfg.PublicURL, "http://127.") && !strings.HasPrefix(cfg.PublicURL, "http://[::1]") {
			log.Info("the gateway must allow this address to sign in; set KEERA_CHAT_URL=" +
				cfg.PublicURL + " on Keera Gateway")
		}
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return fmt.Errorf("listening on %s: %w", cfg.Addr, err)
	case <-ctx.Done():
	}

	log.Info("shutting down")
	// Replies still streaming get a little while to finish.
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	// config.Load has already refused anything else.
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
