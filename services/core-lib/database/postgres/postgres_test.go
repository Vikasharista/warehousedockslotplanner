package postgres

import (
	"testing"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitRequiresAReachableDatabase(t *testing.T) {
	assert.Error(t, InitPostgres(nil))
	assert.Error(t, InitPostgres(&PostgresConnectionConfig{}))
	assert.Error(t, InitPostgres(&PostgresConnectionConfig{DSN: "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1"}))
}

func TestInitAndReplace(t *testing.T) {
	_, dsn := testdb.Open(t)
	require.NoError(t, InitPostgres(&PostgresConnectionConfig{DSN: dsn, MaxIdleConnections: 1, MaxOpenConnections: 2}))
	first := GetDatabase()
	var one int
	require.NoError(t, first.Raw("SELECT 1").Scan(&one).Error)
	assert.Equal(t, 1, one)

	require.NoError(t, InitPostgres(&PostgresConnectionConfig{DSN: dsn}))
	assert.NotSame(t, first, GetDatabase())
	sqlDB, err := GetDatabase().DB()
	require.NoError(t, err)
	assert.Equal(t, 0, sqlDB.Stats().OpenConnections-sqlDB.Stats().InUse-sqlDB.Stats().Idle)
	t.Cleanup(func() { _ = sqlDB.Close() })
}
