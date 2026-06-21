package app

import (
	"context"
	"fmt"
	"net/http"

	"github.com/rs/zerolog/log"

	"github.com/Daofengql/tun-over-ws/internal/admin"
	"github.com/Daofengql/tun-over-ws/internal/config"
	"github.com/Daofengql/tun-over-ws/internal/logger"
	"github.com/Daofengql/tun-over-ws/internal/relay"
	"github.com/Daofengql/tun-over-ws/internal/store"
)

// RunServer starts the relay server and admin console with the provided YAML configuration.
func RunServer(ctx context.Context, configPath string) error {
	cfg, err := config.LoadServerConfig(configPath)
	if err != nil {
		return err
	}

	db, err := store.NewSQLiteStore(cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if cfg.Admin.Username != "" && cfg.Admin.Password != "" {
		hash, err := admin.HashPassword(cfg.Admin.Password)
		if err != nil {
			return fmt.Errorf("hash admin password: %w", err)
		}
		if err := db.EnsureAdmin(ctx, cfg.Admin.Username, hash); err != nil {
			return fmt.Errorf("ensure admin user: %w", err)
		}
	}

	srv, err := relay.NewServer(cfg, db)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		srv.HandleTunnel(ctx, w, r)
	})

	jwtSecret := cfg.Admin.JWTSecret
	if jwtSecret == "" {
		jwtSecret, err = admin.GenerateRandomToken(32)
		if err != nil {
			return fmt.Errorf("generate jwt secret: %w", err)
		}
		logger.Logger.Warn().Msg("admin.jwt_secret is empty; web sessions will be invalidated on restart")
	}
	adminHandler := admin.NewHandler(db, cfg, jwtSecret, logger.Logger)
	adminHandler.RegisterRoutes(mux)

	logger.Logger.Info().
		Str("listen", cfg.Listen).
		Str("overlay", cfg.OverlayCIDR).
		Msg("starting relay server")

	server := &http.Server{
		Addr:    cfg.Listen,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		if err := server.Shutdown(context.Background()); err != nil {
			log.Warn().Err(err).Msg("shutdown server")
		}
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
