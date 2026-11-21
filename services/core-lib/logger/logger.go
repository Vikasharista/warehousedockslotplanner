// Package logger owns the process-wide zap logger.
package logger

import (
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	mu     sync.RWMutex
	global = zap.NewNop()
)

// Init builds a JSON production logger at level (-1 debug, 0 info, 1 warn, 2 error) and makes it global.
func Init(level int) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zapcore.Level(level))
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	l, err := cfg.Build()
	if err != nil {
		return nil, err
	}
	mu.Lock()
	global = l
	mu.Unlock()
	return l, nil
}

// GetLogger returns the global logger; before Init it discards everything.
func GetLogger() *zap.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return global
}
