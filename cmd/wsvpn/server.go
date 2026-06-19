package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/daofeng/ws-vpn-go/internal/config"
	"github.com/daofeng/ws-vpn-go/internal/logger"
	"github.com/daofeng/ws-vpn-go/internal/relay"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run as relay server",
	RunE:  runServer,
}

func init() {
	serverCmd.Flags().StringP("config", "c", "", "server config file path")
	rootCmd.AddCommand(serverCmd)
}

func runServer(cmd *cobra.Command, args []string) error {
	configPath, _ := cmd.Flags().GetString("config")
	if configPath == "" {
		log.Fatal().Msg("--config is required")
	}

	cfg, err := config.LoadServerConfig(configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("load config")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv, err := relay.NewServer(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("create server")
	}

	logger.Logger.Info().
		Str("listen", cfg.Listen).
		Str("overlay", cfg.OverlayCIDR).
		Msg("starting relay server")

	return srv.ListenAndServe(ctx)
}
