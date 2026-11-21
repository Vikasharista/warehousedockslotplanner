// Package config loads YAML configuration into the shared Viper instance.
//
// Values come from <BaseConfigPath>/<BaseConfigFile>.yaml, then the optional override file, then environment
// variables: APP_ plus the key in upper case with dots as underscores (database.postgres.dsn ->
// APP_DATABASE_POSTGRES_DSN). Secrets belong in environment variables or the override file, never in Git.
package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// EnvPrefix is the prefix of environment variables that override config keys.
const EnvPrefix = "APP"

// Viper is the shared configuration. Tests may replace it.
var Viper *viper.Viper

// InitConfigParams names the base file and an optional override file (both without the .yaml extension).
type InitConfigParams struct {
	BaseConfigPath     string
	BaseConfigFile     string
	OverrideConfigPath string
	OverrideConfigFile string
}

// Init reads the configuration into Viper.
func Init(params InitConfigParams) error {
	if params.BaseConfigPath == "" || params.BaseConfigFile == "" {
		return fmt.Errorf("config: BaseConfigPath and BaseConfigFile are required")
	}
	v := viper.New()
	v.SetConfigType("yaml")
	v.SetConfigFile(filepath.Join(params.BaseConfigPath, params.BaseConfigFile+".yaml"))
	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("config: read base: %w", err)
	}
	if params.OverrideConfigPath != "" && params.OverrideConfigFile != "" {
		v.SetConfigFile(filepath.Join(params.OverrideConfigPath, params.OverrideConfigFile+".yaml"))
		if err := v.MergeInConfig(); err != nil {
			return fmt.Errorf("config: read override: %w", err)
		}
	}
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	Viper = v
	return nil
}
