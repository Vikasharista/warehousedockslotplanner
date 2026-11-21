package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaseOverrideAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "default.yaml"),
		[]byte("server:\n  port: 8080\n  name: base\ndatabase:\n  postgres:\n    dsn: base-dsn\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local.yaml"), []byte("server:\n  name: local\n"), 0o600))
	t.Setenv("APP_DATABASE_POSTGRES_DSN", "env-dsn")

	require.NoError(t, Init(InitConfigParams{BaseConfigPath: dir, BaseConfigFile: "default",
		OverrideConfigPath: dir, OverrideConfigFile: "local"}))
	assert.Equal(t, 8080, Viper.GetInt("server.port"))
	assert.Equal(t, "local", Viper.GetString("server.name"))
	assert.Equal(t, "env-dsn", Viper.GetString("database.postgres.dsn"))
}

func TestMissingFilesAreErrors(t *testing.T) {
	dir := t.TempDir()
	assert.Error(t, Init(InitConfigParams{}))
	assert.Error(t, Init(InitConfigParams{BaseConfigPath: dir, BaseConfigFile: "nope"}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "default.yaml"), []byte("a: 1\n"), 0o600))
	assert.Error(t, Init(InitConfigParams{BaseConfigPath: dir, BaseConfigFile: "default",
		OverrideConfigPath: dir, OverrideConfigFile: "nope"}))
}
