// Package transaction runs repository work in one database transaction, carried in the context so nested
// repository calls join it.
package transaction

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Func is work to run inside a transaction; it must use the ctx it is given.
type Func func(ctx context.Context) error

// Executor is embedded in every repository interface, so services can group repository calls in one
// transaction. Repository implementations embed *GormExecutor, which also gives them GetDB.
type Executor interface {
	// DoInTransaction runs fn in a transaction, or inside the caller's transaction when ctx already has one.
	// opts are accepted for interface compatibility and ignored.
	DoInTransaction(ctx context.Context, fn Func, opts ...interface{}) error
}

type txKey struct{}

// GormExecutor implements Executor on a GORM pool.
type GormExecutor struct {
	db *gorm.DB
}

// NewGormExecutor wraps db. A nil db is allowed (tests with stubbed repositories); using it fails cleanly.
func NewGormExecutor(db *gorm.DB) *GormExecutor { return &GormExecutor{db: db} }

// GetDB returns the transaction carried by ctx, or the pool bound to ctx.
func (e *GormExecutor) GetDB(ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok && tx != nil {
		return tx.WithContext(ctx)
	}
	if e == nil || e.db == nil {
		return nil
	}
	return e.db.WithContext(ctx)
}

func (e *GormExecutor) DoInTransaction(ctx context.Context, fn Func, _ ...interface{}) error {
	if _, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return fn(ctx)
	}
	if e == nil || e.db == nil {
		return errors.New("transaction: database not initialised")
	}
	return e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}
