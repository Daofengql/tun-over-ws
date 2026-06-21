package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/Daofengql/tun-over-ws/internal/app"
	"github.com/Daofengql/tun-over-ws/internal/logger"
	"github.com/spf13/cobra"
)

var configPath string

var rootCmd = &cobra.Command{
	Use:   "wsvpns",
	Short: "WebSocket L3 VPN relay server",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		level, _ := cmd.Flags().GetString("log-level")
		logger.Setup(level)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return app.RunServer(ctx, configPath)
	},
}

func init() {
	rootCmd.PersistentFlags().String("log-level", "info", "log level (trace/debug/info/warn/error)")
	rootCmd.Flags().StringVarP(&configPath, "config", "c", "", "server config file path")
	rootCmd.MarkFlagRequired("config")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		logger.Logger.Fatal().Err(err).Msg("command failed")
	}
}
