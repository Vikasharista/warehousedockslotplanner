// Package cmd is every service's command line: `serve`, `migrate up [n]`, `migrate down [n]`.
package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"
)

// ExecuteParams wires the commands to the service.
type ExecuteParams struct {
	// Dsn is the Postgres connection string (URL or key=value form) used by migrate.
	Dsn string
	// ServeFunc starts the service and blocks while it runs.
	ServeFunc func() error
	// MigrationsPath is the migrations directory; default "migrations".
	MigrationsPath string
}

// Execute runs the command line and exits non-zero on error.
func Execute(p *ExecuteParams) {
	if err := NewRootCommand(p).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// NewRootCommand builds the command tree; exported for tests.
func NewRootCommand(p *ExecuteParams) *cobra.Command {
	root := &cobra.Command{Use: "service", SilenceUsage: true}
	root.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Start the service",
		RunE: func(*cobra.Command, []string) error {
			if p.ServeFunc == nil {
				return errors.New("no ServeFunc configured")
			}
			return p.ServeFunc()
		},
	})
	migrateCmd := &cobra.Command{Use: "migrate", Short: "Apply or roll back database migrations"}
	migrateCmd.AddCommand(&cobra.Command{
		Use:   "up [n]",
		Short: "Apply all pending migrations, or the next n",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runMigrate(p, args, true)
		},
	})
	migrateCmd.AddCommand(&cobra.Command{
		Use:   "down [n]",
		Short: "Roll back the last migration, or the last n",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runMigrate(p, args, false)
		},
	})
	root.AddCommand(migrateCmd)
	return root
}

func runMigrate(p *ExecuteParams, args []string, up bool) error {
	n := 0
	if len(args) == 1 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v <= 0 {
			return fmt.Errorf("n must be a positive integer, got %q", args[0])
		}
		n = v
	}
	dbURL, err := MigrationURL(p.Dsn)
	if err != nil {
		return err
	}
	path := p.MigrationsPath
	if path == "" {
		path = "migrations"
	}
	m, err := migrate.New("file://"+path, dbURL)
	if err != nil {
		return fmt.Errorf("open migrations: %w", err)
	}
	defer m.Close()
	switch {
	case up && n == 0:
		err = m.Up()
	case up:
		err = m.Steps(n)
	case n == 0:
		err = m.Steps(-1) // never everything by default
	default:
		err = m.Steps(-n)
	}
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}

var sslModeRE = regexp.MustCompile(`(?:^|\s)sslmode=(\S+)`)

// MigrationURL turns a key=value DSN into the postgres:// URL golang-migrate needs; URLs pass through.
func MigrationURL(dsn string) (string, error) {
	if dsn == "" {
		return "", errors.New("database DSN is empty")
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return dsn, nil
	}
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return "", fmt.Errorf("parse DSN: %w", err)
	}
	u := &url.URL{Scheme: "postgres", Host: fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), Path: "/" + cfg.Database}
	if cfg.Password != "" {
		u.User = url.UserPassword(cfg.User, cfg.Password)
	} else {
		u.User = url.User(cfg.User)
	}
	// Keep the DSN's own sslmode (verify-full stays verify-full); without one, no TLS means disable.
	q := url.Values{}
	if m := sslModeRE.FindStringSubmatch(dsn); m != nil {
		q.Set("sslmode", m[1])
	} else if cfg.TLSConfig == nil {
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
