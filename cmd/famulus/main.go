// Command famulus polls GitHub issues labeled for agents, runs a coding agent on each and
// opens pull requests as a GitHub App.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/daniil4545/famulus/internal/agent"
	"github.com/daniil4545/famulus/internal/config"
	"github.com/daniil4545/famulus/internal/github"
	"github.com/daniil4545/famulus/internal/panel"
	"github.com/daniil4545/famulus/internal/state"
	"github.com/daniil4545/famulus/internal/workspace"
	"github.com/daniil4545/famulus/internal/yard"
)

func main() {
	if err := run(); err != nil {
		slog.Error("famulus stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "famulus.toml", "path to the config file")
	once := flag.Bool("once", false, "make one pass and exit")
	flag.Parse()

	recent := panel.NewLog(300)
	log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stdout, recent), nil))
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	appID := os.Getenv("FAMULUS_APP_ID")
	if appID == "" {
		return errors.New("FAMULUS_APP_ID is not set")
	}
	key, err := github.ParseKey(os.Getenv("FAMULUS_APP_KEY_B64"))
	if err != nil {
		return fmt.Errorf("FAMULUS_APP_KEY_B64: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gh := github.NewClient("https://api.github.com", appID, key)
	name, email, err := gh.Identity(ctx)
	if err != nil {
		return fmt.Errorf("bot identity: %w", err)
	}
	statePath := filepath.Join(cfg.WorkDir, "state.json")
	store, err := state.Load(statePath)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	health := panel.NewHealth(time.Now())
	y := &yard.Yard{
		Config: cfg,
		GitHub: gh,
		Work:   workspace.New(cfg.WorkDir, "https://github.com/", gh.Token, name, email),
		Agents: agent.CLI{},
		State:  store,
		Log:    log,
		Passed: health.Passed,
	}
	log.Info("famulus started", "bot", name, "owner", cfg.Owner, "repos", len(cfg.Repos), "interval", cfg.Interval)
	if *once {
		y.Pass(ctx)
		return nil
	}
	if cfg.Panel != "" {
		// the panel reads state.json from disk, so it shares no memory with the loop
		srv := &http.Server{
			Addr:              cfg.Panel,
			Handler:           &panel.Panel{StatePath: statePath, Health: health, Log: recent, Bot: name, Now: time.Now, Logger: log},
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("panel stopped", "addr", cfg.Panel, "err", err)
			}
		}()
		defer func() { _ = srv.Close() }() // the process exits right after
		log.Info("panel listening", "addr", cfg.Panel)
	}
	y.Run(ctx)
	return nil
}
