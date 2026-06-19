package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/Daofengql/tun-over-ws/internal/config"
	"github.com/Daofengql/tun-over-ws/internal/conn"
	"github.com/Daofengql/tun-over-ws/internal/logger"
)

var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Run as VPN client",
	RunE:  runClient,
}

func init() {
	clientCmd.Flags().StringP("config", "c", "", "client config file path")
	rootCmd.AddCommand(clientCmd)
}

func runClient(cmd *cobra.Command, args []string) error {
	configPath, _ := cmd.Flags().GetString("config")
	if configPath == "" {
		log.Fatal().Msg("--config is required")
	}

	cfg, err := config.LoadClientConfig(configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("load config")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool := conn.NewPool(cfg.ServerURL, cfg.UUID, cfg.Token, cfg.TUN.Name, cfg.TUN.MTU)

	if err := pool.Connect(ctx); err != nil {
		log.Fatal().Err(err).Msg("connect")
	}

	logger.Logger.Info().
		Str("vip", pool.VirtualIP().String()).
		Str("tun", pool.TunName()).
		Msg("client ready")

	return pool.Run(ctx)
}
