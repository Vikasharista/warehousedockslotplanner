package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func run(p *ExecuteParams, args ...string) error {
	root := NewRootCommand(p)
	root.SetArgs(args)
	root.SetOut(new(nopWriter))
	root.SetErr(new(nopWriter))
	return root.Execute()
}

type nopWriter struct{}

func (*nopWriter) Write(b []byte) (int, error) { return len(b), nil }

func TestServeRunsTheServeFunc(t *testing.T) {
	served := false
	require.NoError(t, run(&ExecuteParams{ServeFunc: func() error { served = true; return nil }}, "serve"))
	assert.True(t, served)

	boom := errors.New("port in use")
	assert.ErrorIs(t, run(&ExecuteParams{ServeFunc: func() error { return boom }}, "serve"), boom)
	assert.Error(t, run(&ExecuteParams{}, "serve"))
}

func TestMigrateRejectsBadArguments(t *testing.T) {
	p := &ExecuteParams{Dsn: "postgres://u@localhost/db"}
	for _, args := range [][]string{{"migrate", "up", "x"}, {"migrate", "down", "0"}, {"migrate", "up", "1", "2"}} {
		assert.Error(t, run(p, args...), "%v", args)
	}
	assert.Error(t, run(&ExecuteParams{}, "migrate", "up"), "empty DSN")
}

func TestMigrationURL(t *testing.T) {
	u, err := MigrationURL("postgres://a:b@h:5/d?sslmode=require")
	require.NoError(t, err)
	assert.Equal(t, "postgres://a:b@h:5/d?sslmode=require", u)

	u, err = MigrationURL("host=db port=5433 user=app password=p@ss dbname=widgets sslmode=disable")
	require.NoError(t, err)
	assert.Equal(t, "postgres://app:p%40ss@db:5433/widgets?sslmode=disable", u)

	u, err = MigrationURL("host=db user=app dbname=widgets sslmode=verify-full")
	require.NoError(t, err)
	assert.Equal(t, "postgres://app@db:5432/widgets?sslmode=verify-full", u)

	u, err = MigrationURL("host=db user=app dbname=widgets")
	require.NoError(t, err)
	assert.Equal(t, "postgres://app@db:5432/widgets", u, "no sslmode given: the driver default applies")

	_, err = MigrationURL("")
	assert.Error(t, err)
	_, err = MigrationURL("host=db port=notaport")
	assert.Error(t, err)
}

func TestMigrateUpAndDownAgainstPostgres(t *testing.T) {
	db, dsn := testdb.Open(t)
	var schema string
	require.NoError(t, db.Raw("SELECT current_schema()").Scan(&schema).Error)
	url, err := MigrationURL(dsn)
	require.NoError(t, err)
	url += "&search_path=" + schema

	dir := t.TempDir()
	write := func(name, sql string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(sql), 0o600))
	}
	write("000001_a.up.sql", "CREATE TABLE a (id int);")
	write("000001_a.down.sql", "DROP TABLE a;")
	write("000002_b.up.sql", "CREATE TABLE b (id int);")
	write("000002_b.down.sql", "DROP TABLE b;")
	p := &ExecuteParams{Dsn: url, MigrationsPath: dir}
	tables := func() (n int64) {
		db.Raw("SELECT count(*) FROM information_schema.tables WHERE table_schema = ? AND table_name IN ('a','b')", schema).Scan(&n)
		return n
	}

	require.NoError(t, run(p, "migrate", "up", "1"))
	assert.EqualValues(t, 1, tables())
	require.NoError(t, run(p, "migrate", "up"))
	assert.EqualValues(t, 2, tables())
	require.NoError(t, run(p, "migrate", "up"), "no change is not an error")
	require.NoError(t, run(p, "migrate", "down"), "down without n rolls back one")
	assert.EqualValues(t, 1, tables())
	require.NoError(t, run(p, "migrate", "down", "1"))
	assert.EqualValues(t, 0, tables())
	assert.Error(t, run(&ExecuteParams{Dsn: url, MigrationsPath: filepath.Join(dir, "missing")}, "migrate", "up"))
}
