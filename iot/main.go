package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	app := newService(config, logger)
	var broker ioCloser
	if config.MQTTURL == "" {
		broker, err = startEmbeddedBroker(app)
		if err != nil {
			return err
		}
	} else {
		broker = startExternalBroker(ctx, app)
	}
	defer func() {
		cancel()
		app.hub.close()
		_ = broker.Close()
	}()
	var web fs.FS = os.DirFS(config.WebDir)
	server := &http.Server{
		Addr: config.HTTPAddr, Handler: app.handler(web), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	listener, err := net.Listen("tcp", config.HTTPAddr)
	if err != nil {
		return err
	}
	go app.watchFreshness(ctx)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	logger.Info("mixer service ready", "http", "http://"+listener.Addr().String(), "broker_mode", app.snapshot().Broker.Mode)
	select {
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serveErrors:
		if isNormalHTTPShutdown(err) {
			return nil
		}
		return err
	}
}

type ioCloser interface{ Close() error }
