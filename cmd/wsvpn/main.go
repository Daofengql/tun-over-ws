package main

import (
	"github.com/Daofengql/tun-over-ws/internal/logger"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "wsvpn",
	Short: "WebSocket L3 VPN",
	Long:  "A centralized WebSocket L3 VPN for overlay networking and exit gateway.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		level, _ := cmd.Flags().GetString("log-level")
		logger.Setup(level)
	},
}

func init() {
	rootCmd.PersistentFlags().String("log-level", "info", "log level (trace/debug/info/warn/error)")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		logger.Logger.Fatal().Err(err).Msg("command failed")
	}
}
