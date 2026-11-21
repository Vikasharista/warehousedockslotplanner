package transaction

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type row struct {
	ID   int
	Name string
}

func TestNilDatabaseFailsCleanly(t *testing.T) {
	e := NewGormExecutor(nil)
	assert.Nil(t, e.GetDB(context.Background()))
	assert.Error(t, e.DoInTransaction(context.Background(), func(context.Context) error { return nil }))
	var nilExec *GormExecutor
	assert.Nil(t, nilExec.GetDB(context.Background()))
}

func TestCommitRollbackAndNesting(t *testing.T) {
	db, _ := testdb.Open(t)
	require.NoError(t, db.AutoMigrate(&row{}))
	e := NewGormExecutor(db)
	var _ Executor = e
	ctx := context.Background()
	count := func() (n int64) { db.Model(&row{}).Count(&n); return n }

	require.NoError(t, e.DoInTransaction(ctx, func(ctx context.Context) error {
		return e.GetDB(ctx).Create(&row{Name: "a"}).Error
	}))
	assert.EqualValues(t, 1, count())

	boom := stderrors.New("boom")
	err := e.DoInTransaction(ctx, func(ctx context.Context) error {
		require.NoError(t, e.GetDB(ctx).Create(&row{Name: "b"}).Error)
		// A nested call joins the outer transaction, so its write rolls back too.
		require.NoError(t, e.DoInTransaction(ctx, func(ctx context.Context) error {
			return e.GetDB(ctx).Create(&row{Name: "c"}).Error
		}))
		return boom
	})
	assert.ErrorIs(t, err, boom)
	assert.EqualValues(t, 1, count())
}
