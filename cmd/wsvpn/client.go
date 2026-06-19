package main

import (
	"context"
	"net/netip"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/daofeng/ws-vpn-go/internal/config"
	"github.com/daofeng/ws-vpn-go/internal/conn"
	"github.com/daofeng/ws-vpn-go/internal/logger"
)

var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Run as VPN client",
	RunE:  runClient,
}

func init() {
	clientCmd.Flags().StringP("config", "c", "", "client config file path")
	clientCmd.Flags().String("send-to", "", "send a test packet to this IP and exit (e.g. 10.66.0.3)")
	rootCmd.AddCommand(clientCmd)
}

func runClient(cmd *cobra.Command, args []string) error {
	configPath, _ := cmd.Flags().GetString("config")
	if configPath == "" {
		log.Fatal().Msg("--config is required")
	}
	sendTo, _ := cmd.Flags().GetString("send-to")

	cfg, err := config.LoadClientConfig(configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("load config")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := conn.New(cfg.ServerURL, cfg.UUID, cfg.Token, cfg.TUN.Name)

	if err := client.Connect(ctx); err != nil {
		log.Fatal().Err(err).Msg("connect")
	}

	logger.Logger.Info().
		Str("vip", client.VirtualIP().String()).
		Str("tun", cfg.TUN.Name).
		Msg("client ready")

	// If --send-to is specified, send a test ICMP-like packet and exit.
	if sendTo != "" {
		return sendTestPacket(ctx, client, sendTo)
	}

	// Run TUN <-> WebSocket pump.
	return client.Run(ctx)
}

func sendTestPacket(ctx context.Context, c *conn.Conn, dstIP string) error {
	dst, err := netip.ParseAddr(dstIP)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid --send-to IP")
	}
	src := c.VirtualIP()

	pkt := buildTestIPv4Packet(src, dst)
	logger.Logger.Info().
		Str("src", src.String()).
		Str("dst", dst.String()).
		Int("bytes", len(pkt)).
		Msg("sending test packet")

	// TODO: Send via TUN instead of WebSocket for proper testing.
	// For now, just wait for TUN traffic.

	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
	}
	return nil
}

func buildTestIPv4Packet(src, dst netip.Addr) []byte {
	pkt := make([]byte, 28)
	pkt[0] = 0x45
	pkt[2] = 0
	pkt[3] = 28
	pkt[8] = 64
	pkt[9] = 255
	s4 := src.As4()
	d4 := dst.As4()
	copy(pkt[12:16], s4[:])
	copy(pkt[16:20], d4[:])
	copy(pkt[20:28], []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04})
	return pkt
}
