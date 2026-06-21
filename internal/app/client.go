package app

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/Daofengql/tun-over-ws/internal/config"
	"github.com/Daofengql/tun-over-ws/internal/conn"
	"github.com/Daofengql/tun-over-ws/internal/logger"
)

// RunClient starts the VPN client with the provided YAML configuration.
func RunClient(ctx context.Context, configPath string) error {
	cfg, err := config.LoadClientConfig(configPath)
	if err != nil {
		return err
	}

	auth := conn.NewDeviceAuth(cfg.ServerURL, cfg.DeviceDir)
	if err := auth.LoadOrCreateCredentials(); err != nil {
		return fmt.Errorf("load device credentials: %w", err)
	}

	if !auth.HasValidKey() {
		if err := auth.RefreshKey(); err != nil {
			log.Info().Msg("No valid access key, starting device authorization...")

			authURL, sessionCode, err := auth.InitDeviceAuth()
			if err != nil {
				return fmt.Errorf("init device auth: %w", err)
			}

			fmt.Printf("\n请在浏览器中访问以下链接来授权此设备:\n%s\n\n", authURL)

			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(3 * time.Second):
					status, err := auth.PollAuthStatus(sessionCode)
					if err != nil {
						log.Error().Err(err).Msg("poll auth status")
						continue
					}

					switch status {
					case "approved":
						log.Info().Msg("Device authorized successfully!")
					case "expired":
						return fmt.Errorf("authorization session expired, please restart")
					default:
						log.Info().Str("status", status).Msg("Waiting for authorization...")
						continue
					}
				}
				break
			}
		}
	}

	pool := conn.NewPool(cfg.ServerURL, auth.GetDeviceID(), auth.GetAccessKey(), cfg.TUN.Name, cfg.TUN.MTU)
	if err := pool.Connect(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	logger.Logger.Info().
		Str("vip", pool.VirtualIP().String()).
		Str("tun", pool.TunName()).
		Msg("client ready")

	return pool.Run(ctx)
}
