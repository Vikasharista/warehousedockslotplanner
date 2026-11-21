package logger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

func TestNopBeforeInitThenConfiguredLevel(t *testing.T) {
	assert.NotNil(t, GetLogger())
	l, err := Init(1)
	require.NoError(t, err)
	assert.Same(t, l, GetLogger())
	assert.False(t, l.Core().Enabled(zapcore.InfoLevel))
	assert.True(t, l.Core().Enabled(zapcore.WarnLevel))
}
