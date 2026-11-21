// Package loader builds per-request dataloaders that batch GetByIDs calls.
package loader

import (
	"context"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/graph-gophers/dataloader/v7"
)

// Keyer is a result that knows the key it answers.
type Keyer[K comparable] interface {
	Key() K
}

// Builder configures a loader around a batch fetch.
type Builder[K comparable, V Keyer[K]] struct {
	fetch func(context.Context, []K) ([]V, error)
	opts  []dataloader.Option[K, V]
}

// NewBuilder wraps fetch, which returns the rows found for the given keys in any order.
func NewBuilder[K comparable, V Keyer[K]](fetch func(context.Context, []K) ([]V, error)) *Builder[K, V] {
	return &Builder[K, V]{fetch: fetch}
}

// WithOptions adds dataloader options (batch size, wait, cache).
func (b *Builder[K, V]) WithOptions(opts ...dataloader.Option[K, V]) *Builder[K, V] {
	b.opts = append(b.opts, opts...)
	return b
}

// Build returns a loader. Each key gets its row, or RECORD_NOT_FOUND when fetch did not return it;
// a failed fetch fails every key in the batch.
func (b *Builder[K, V]) Build() *dataloader.Loader[K, V] {
	batch := func(ctx context.Context, keys []K) []*dataloader.Result[V] {
		results := make([]*dataloader.Result[V], len(keys))
		rows, err := b.fetch(ctx, keys)
		if err != nil {
			for i := range keys {
				results[i] = &dataloader.Result[V]{Error: err}
			}
			return results
		}
		byKey := make(map[K]V, len(rows))
		for _, row := range rows {
			byKey[row.Key()] = row
		}
		for i, k := range keys {
			if row, ok := byKey[k]; ok {
				results[i] = &dataloader.Result[V]{Data: row}
			} else {
				results[i] = &dataloader.Result[V]{Error: errors.NewRecordNotFoundError("record not found")}
			}
		}
		return results
	}
	return dataloader.NewBatchedLoader(batch, b.opts...)
}
