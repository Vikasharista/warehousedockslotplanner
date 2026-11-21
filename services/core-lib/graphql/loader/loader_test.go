package loader

import (
	"context"
	stderrors "errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/graph-gophers/dataloader/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type widget struct{ ID int }

func (w *widget) Key() int { return w.ID }

func TestLoaderBatchesAndReportsMissingKeys(t *testing.T) {
	var calls int32
	var batch []int
	l := NewBuilder(func(_ context.Context, ids []int) ([]*widget, error) {
		atomic.AddInt32(&calls, 1)
		batch = ids
		// Out of order, and 3 is missing.
		return []*widget{{ID: 2}, {ID: 1}}, nil
	}).WithOptions(dataloader.WithWait[int, *widget](5 * time.Millisecond)).Build()

	ctx := context.Background()
	t1, t2, t3 := l.Load(ctx, 1), l.Load(ctx, 2), l.Load(ctx, 3)
	w1, err := t1()
	require.NoError(t, err)
	w2, err := t2()
	require.NoError(t, err)
	_, err = t3()

	assert.Equal(t, 1, w1.ID)
	assert.Equal(t, 2, w2.ID)
	assert.Equal(t, errors.RecordNotFoundErrorCode, errors.CodeOf(err))
	assert.EqualValues(t, 1, calls)
	assert.ElementsMatch(t, []int{1, 2, 3}, batch)
}

func TestAFailedFetchFailsEveryKey(t *testing.T) {
	boom := stderrors.New("db down")
	l := NewBuilder(func(context.Context, []int) ([]*widget, error) { return nil, boom }).Build()
	_, errs := l.LoadMany(context.Background(), []int{1, 2})()
	require.Len(t, errs, 2)
	for _, err := range errs {
		assert.ErrorIs(t, err, boom)
	}
}
