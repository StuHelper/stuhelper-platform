// Package application assembles modules, adapters and the HTTP lifecycle with Fx.
package application

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/StuHelper/stuhelper-platform/internal/api/gen"
	"github.com/StuHelper/stuhelper-platform/module"
	"github.com/StuHelper/stuhelper-platform/transport"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

type Config struct {
	Address string
	Enabled []string
}

func LoadConfig() (Config, error) {
	address := os.Getenv("STUHELPER_LISTEN")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return Config{}, errors.New("STUHELPER_LISTEN must be host:port")
	}
	modules := os.Getenv("STUHELPER_MODULES")
	if modules == "" {
		modules = "platform"
	}
	enabled := strings.Split(modules, ",")
	for i := range enabled {
		enabled[i] = strings.TrimSpace(enabled[i])
	}
	return Config{Address: address, Enabled: enabled}, nil
}

type Modules struct {
	Definitions   []module.Definition
	Routes        []transport.Route
	Authenticator transport.Authenticator
	Authorizer    transport.Authorizer
}

func New(config Config, modules Modules) (*fx.App, error) {
	if !slices.Contains(config.Enabled, "platform") {
		return nil, errors.New("platform must be enabled")
	}
	definitions := append([]module.Definition{{Critical: true, Descriptor: module.Descriptor{ID: "platform", Version: "development", API: 1, Capabilities: []string{"platform.status", "platform.modules"}}}}, modules.Definitions...)
	registry, err := module.New(definitions, config.Enabled)
	if err != nil {
		return nil, err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	routes := []transport.Route{
		{Method: "GET", Path: "/api/v1/system/capabilities", Module: "platform", Capability: "platform.status", Access: transport.Public, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			transport.JSON(w, 200, gen.Capabilities{ApiVersion: gen.N1, Capabilities: registry.Capabilities(ctx)})
		})},
		{Method: "GET", Path: "/api/v1/admin/modules", Module: "platform", Capability: "platform.modules", Access: transport.Protected, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			transport.JSON(w, 200, registry.StatusesContext(r.Context()))
		})},
	}
	routes = append(routes, modules.Routes...)
	handler, err := transport.NewHandler(registry, routes, modules.Authenticator, modules.Authorizer, logger)
	if err != nil {
		return nil, err
	}
	application := fx.New(
		fx.WithLogger(func() fxevent.Logger {
			fxLogger := &fxevent.SlogLogger{Logger: logger}
			fxLogger.UseLogLevel(slog.LevelDebug)
			return fxLogger
		}),
		fx.StartTimeout(15*time.Second), fx.StopTimeout(15*time.Second),
		fx.Invoke(func(lc fx.Lifecycle, shutdown fx.Shutdowner) {
			registry.RegisterLifecycle(lc)
			server := &http.Server{Addr: config.Address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
			lc.Append(fx.Hook{
				OnStart: func(context.Context) error {
					listener, err := net.Listen("tcp", server.Addr)
					if err != nil {
						return errors.New("HTTP listener unavailable")
					}
					logger.Info("platform listening", "address", listener.Addr().String())
					go func() {
						if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
							logger.Error("HTTP server stopped unexpectedly")
							_ = shutdown.Shutdown(fx.ExitCode(1))
						}
					}()
					return nil
				},
				OnStop: func(ctx context.Context) error {
					if err := server.Shutdown(ctx); err != nil {
						_ = server.Close()
						return err
					}
					return nil
				},
			})
		}),
	)
	return application, application.Err()
}
