// Package testdb opens a throwaway Postgres schema for integration tests. Tests skip when TEST_DATABASE_DSN is unset.
package testdb

import (
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open returns a connection whose search_path is a fresh schema, dropped when the test ends, and its DSN.
func Open(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN is not set")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	schema := fmt.Sprintf("corelib_test_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	scoped := dsn + " search_path=" + schema
	db, err := gorm.Open(postgres.Open(scoped), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open scoped: %v", err)
	}
	t.Cleanup(func() {
		if sql, err := db.DB(); err == nil {
			_ = sql.Close()
		}
		admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		if sql, err := admin.DB(); err == nil {
			_ = sql.Close()
		}
	})
	return db, scoped
}
