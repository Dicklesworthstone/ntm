package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/integrations/pt"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/serve"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func newServeCmd() *cobra.Command {
	opts := serveOptions{
		Host:     "127.0.0.1",
		Port:     serve.DefaultPort,
		AuthMode: "local",
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start HTTP server with REST API and event streaming",
		Long: `Start a local HTTP server providing REST API and SSE event streaming
for dashboards, monitoring tools, and robot consumption.

API Endpoints:
  GET /api/sessions          List all sessions
  GET /api/sessions/:id      Get session details
  GET /api/sessions/:id/agents  Get agents in session
  GET /api/sessions/:id/events  Get recent events for session
  GET /api/robot/status      Robot status (JSON)
  GET /api/robot/health      Robot health (JSON)
  GET /events                Server-Sent Events stream
  GET /health                Health check

Examples:
  ntm serve                              # Start on default port
  ntm serve --port 8080                  # Start on custom port
  ntm serve --host 0.0.0.0 --auth-mode api_key --api-key $KEY
  ntm serve --auth-mode oidc --oidc-issuer https://issuer --oidc-jwks-url https://issuer/.well-known/jwks.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(opts)
		},
	}

	cmd.Flags().StringVar(&opts.Host, "host", opts.Host, "HTTP bind host (default 127.0.0.1)")
	cmd.Flags().IntVar(&opts.Port, "port", opts.Port, "HTTP server port")
	cmd.Flags().StringVar(&opts.AuthMode, "auth-mode", opts.AuthMode, "Auth mode: local|api_key|oidc|mtls")
	cmd.Flags().StringVar(&opts.APIKey, "api-key", "", "API key for api_key auth mode")
	cmd.Flags().StringVar(&opts.OIDCIssuer, "oidc-issuer", "", "OIDC issuer URL for oidc auth mode")
	cmd.Flags().StringVar(&opts.OIDCAudience, "oidc-audience", "", "OIDC audience for oidc auth mode")
	cmd.Flags().StringVar(&opts.OIDCJWKSURL, "oidc-jwks-url", "", "JWKS URL for oidc auth mode")
	cmd.Flags().StringVar(&opts.MTLSCert, "mtls-cert", "", "Server TLS cert file for mtls auth mode")
	cmd.Flags().StringVar(&opts.MTLSKey, "mtls-key", "", "Server TLS key file for mtls auth mode")
	cmd.Flags().StringVar(&opts.MTLSCA, "mtls-ca", "", "Client CA bundle for mtls auth mode")
	cmd.Flags().StringArrayVar(&opts.CORSAllowOrigins, "cors-allow-origin", nil, "Allowed CORS origins (repeatable). Defaults to localhost only.")
	cmd.Flags().StringVar(&opts.PublicBaseURL, "public-base-url", "", "Public base URL for external clients (optional)")

	return cmd
}

type serveOptions struct {
	Host             string
	Port             int
	PublicBaseURL    string
	AuthMode         string
	APIKey           string
	OIDCIssuer       string
	OIDCAudience     string
	OIDCJWKSURL      string
	MTLSCert         string
	MTLSKey          string
	MTLSCA           string
	CORSAllowOrigins []string
}

func runServe(opts serveOptions) error {
	// Open state store
	stateStore, err := state.Open("")
	if err != nil {
		return fmt.Errorf("open state store: %w", err)
	}
	defer stateStore.Close()

	// Ensure migrations are applied
	if err := stateStore.Migrate(); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	robot.SetAttentionFeed(robot.NewAttentionFeed(
		robot.DefaultAttentionFeedConfig(),
		robot.WithAttentionStore(stateStore),
	))
	feed := robot.GetAttentionFeed()

	ptCfg := config.DefaultProcessTriageConfig()
	if cfg != nil {
		ptCfg = cfg.Integrations.ProcessTriage
	}
	var ptMonitor *pt.HealthMonitor
	if ptCfg.Enabled {
		ptMonitor = pt.InitGlobalMonitor(&ptCfg,
			pt.WithStateChangeCallback(func(change pt.ClassificationStateChange) {
				if feed == nil {
					return
				}
				feed.PublishPTStateChange(change)
			}),
			pt.WithAlertCallback(func(alert pt.Alert) {
				if feed == nil {
					return
				}
				feed.PublishPTAlert(alert)
			}),
		)
		if err := ptMonitor.Start(); err != nil {
			slog.Warn("process triage monitor start failed", "err", err)
			ptMonitor = nil
		} else {
			defer ptMonitor.Stop()
		}
	}

	mode, err := serve.ParseAuthMode(opts.AuthMode)
	if err != nil {
		return err
	}
	serverCfg := serve.Config{
		Host:           opts.Host,
		Port:           opts.Port,
		PublicBaseURL:  opts.PublicBaseURL,
		Version:        Version,
		EventBus:       events.DefaultBus,
		StateStore:     stateStore,
		AllowedOrigins: opts.CORSAllowOrigins,
		Auth: serve.AuthConfig{
			Mode:   mode,
			APIKey: opts.APIKey,
			OIDC: serve.OIDCConfig{
				Issuer:   opts.OIDCIssuer,
				Audience: opts.OIDCAudience,
				JWKSURL:  opts.OIDCJWKSURL,
			},
			MTLS: serve.MTLSConfig{
				CertFile:     opts.MTLSCert,
				KeyFile:      opts.MTLSKey,
				ClientCAFile: opts.MTLSCA,
			},
		},
	}
	if err := serve.ValidateConfig(serverCfg); err != nil {
		return err
	}
	// Create server with default event bus
	srv := serve.New(serverCfg)

	// Setup signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	refreshCtx, refreshCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := robot.RefreshNormalizedProjection(refreshCtx, stateStore, "", ""); err != nil {
		slog.Warn("normalized projection refresh failed", "err", err)
	}
	refreshCancel()

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(ctx, 10*time.Second)
				if err := robot.RefreshNormalizedProjection(refreshCtx, stateStore, "", ""); err != nil {
					slog.Warn("normalized projection refresh failed", "err", err)
				}
				refreshCancel()
			}
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	sigDone := make(chan struct{})
	defer close(sigDone)

	go func() {
		select {
		case <-sigCh:
			fmt.Println("\nReceived shutdown signal")
			cancel()
		case <-sigDone:
		}
	}()

	scheme := "http"
	if mode == serve.AuthModeMTLS {
		scheme = "https"
	}
	slog.Info("server starting",
		"host", opts.Host,
		"port", opts.Port,
		"auth_mode", opts.AuthMode,
		"tls_enabled", mode == serve.AuthModeMTLS,
		"public_base_url", opts.PublicBaseURL,
		"allowed_origins", len(opts.CORSAllowOrigins),
	)
	fmt.Printf("Starting NTM server on %s://%s:%d\n", scheme, opts.Host, opts.Port)
	fmt.Println("Press Ctrl+C to stop")

	return srv.Start(ctx)
}
