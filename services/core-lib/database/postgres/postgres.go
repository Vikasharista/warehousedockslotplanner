// Package postgres owns the process-wide GORM connection pool.
package postgres

import (
	"errors"
	"fmt"
	"sync"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// PostgresConnectionConfig configures the shared pool.
type PostgresConnectionConfig struct {
	DSN                string
	MaxIdleConnections int
	MaxOpenConnections int
	// ConnMaxLifetime defaults to 30 minutes.
	ConnMaxLifetime time.Duration
}

var (
	mu sync.RWMutex
	db *gorm.DB
)

// InitPostgres opens and pings the shared pool. Calling it again replaces the pool.
func InitPostgres(cfg *PostgresConnectionConfig) error {
	if cfg == nil || cfg.DSN == "" {
		return errors.New("postgres: DSN is required")
	}
	conn, err := gorm.Open(gormpostgres.Open(cfg.DSN), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Warn)})
	if err != nil {
		return fmt.Errorf("postgres: open: %w", err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		return fmt.Errorf("postgres: pool: %w", err)
	}
	if cfg.MaxIdleConnections > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConnections)
	}
	if cfg.MaxOpenConnections > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConnections)
	}
	lifetime := cfg.ConnMaxLifetime
	if lifetime == 0 {
		lifetime = 30 * time.Minute
	}
	sqlDB.SetConnMaxLifetime(lifetime)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("postgres: ping: %w", err)
	}
	mu.Lock()
	previous := db
	db = conn
	mu.Unlock()
	if previous != nil {
		if old, err := previous.DB(); err == nil {
			_ = old.Close()
		}
	}
	return nil
}

// GetDatabase returns the shared pool, or nil before InitPostgres.
func GetDatabase() *gorm.DB {
	mu.RLock()
	defer mu.RUnlock()
	return db
}
