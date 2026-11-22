// dock-api serves the plan view and driver holds (hold, confirm, release) for the SetuHaul dock planner.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/cmd"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/config"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/database/postgres"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/logger"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/api"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/auth"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/solver"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/store"
	"go.uber.org/zap"
)

func main() {
	if err := config.Init(config.InitConfigParams{
		BaseConfigPath: envOr("CONFIG_PATH", "config"), BaseConfigFile: "default",
		OverrideConfigPath: os.Getenv("OVERRIDE_CONFIG_PATH"), OverrideConfigFile: os.Getenv("OVERRIDE_CONFIG_FILE"),
	}); err != nil {
		panic(err)
	}
	if _, err := logger.Init(config.Viper.GetInt("application.log_level")); err != nil {
		panic(err)
	}
	cmd.Execute(&cmd.ExecuteParams{Dsn: config.Viper.GetString("database.postgres.dsn"), ServeFunc: serve})
}

func serve() error {
	v := config.Viper
	log := logger.GetLogger()
	if err := postgres.InitPostgres(&postgres.PostgresConnectionConfig{
		DSN:                v.GetString("database.postgres.dsn"),
		MaxIdleConnections: v.GetInt("database.postgres.max_idle_connections"),
		MaxOpenConnections: v.GetInt("database.postgres.max_open_connections"),
	}); err != nil {
		return err
	}
	st := store.New(postgres.GetDatabase(), store.Settings{
		GateBufferMin:          v.GetInt("planning.gate_buffer_min"),
		LowConfidenceMarginMin: v.GetInt("planning.low_confidence_margin_min"),
		HorizonHours:           v.GetInt("planning.horizon_hours"),
		HoldExpiryMin:          v.GetInt("planning.hold_expiry_min"),
	})
	sv := solver.New(v.GetString("solver.url"), v.GetDuration("solver.timeout"))
	au, err := auth.New(st, auth.Settings{
		Secret:       v.GetString("auth.jwt.secret"),
		SessionTTL:   v.GetDuration("auth.session_ttl"),
		SecureCookie: v.GetBool("auth.secure_cookie"),
	})
	if err != nil {
		return err
	}
	accounts, err := auth.ParseAccounts(v.GetString("auth.demo_accounts"))
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		log.Warn("no demo accounts configured (APP_AUTH_DEMO_ACCOUNTS): nobody can sign in")
	}
	if err := au.EnsureAccounts(context.Background(), accounts); err != nil {
		return err
	}
	agentKeys, err := api.ParseAgentKeys(v.GetString("agent.keys"))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sweep(ctx, st, v.GetDuration("holds.sweep_every"))

	srv := &http.Server{Addr: v.GetString("server.address"), Handler: api.Handler(st, sv, au, agentKeys),
		ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("dock-api listening", zap.String("address", srv.Addr))
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// sweep cancels expired driver holds on a timer.
func sweep(ctx context.Context, st *store.Store, every time.Duration) {
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := st.ExpireHolds(ctx)
			if err != nil {
				logger.GetLogger().With(zap.Error(err)).Error("holds.sweep")
			} else if n > 0 {
				logger.GetLogger().Info("expired driver holds", zap.Int("count", n))
			}
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
