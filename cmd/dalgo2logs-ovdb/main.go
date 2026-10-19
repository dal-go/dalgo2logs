package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dal-go/dalgo2logs/ovdbserver"
)

type fileConfig struct {
	Listen  string            `json:"listen"`
	Sources ovdbserver.Config `json:"sources"`
}

func main() {
	path := flag.String("config", "", "trusted JSON source configuration")
	flag.Parse()
	if err := run(*path); err != nil {
		log.Fatal(err)
	}
}

func run(path string) error {
	if path == "" {
		return errors.New("--config is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("configuration is unavailable")
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var cfg fileConfig
	if err := decoder.Decode(&cfg); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return errors.New("configuration contains trailing data")
		}
		return fmt.Errorf("invalid trailing configuration: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:6832"
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil || !loopback(host) {
		return errors.New("listen must use a loopback address and explicit port")
	}
	ownerToken := os.Getenv("OVDB_LOGS_OWNER_TOKEN")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service, err := ovdbserver.New(ctx, cfg.Sources, ownerToken)
	if err != nil {
		return err
	}
	defer service.Close()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	httpServer := &http.Server{Handler: service.Handler, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("OpenVaultDB log sources listening on %s", listener.Addr())
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func loopback(host string) bool {
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}
