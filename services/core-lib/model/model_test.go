package model

import (
	"context"
	"testing"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/internal/testdb"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapScansAndValues(t *testing.T) {
	v, err := Map{"a": 1.0}.Value()
	require.NoError(t, err)
	var m Map
	require.NoError(t, m.Scan(v))
	assert.Equal(t, Map{"a": 1.0}, m)
	require.NoError(t, m.Scan(`{"b":"x"}`))
	assert.Equal(t, Map{"b": "x"}, m)
	require.NoError(t, m.Scan(nil))
	assert.Nil(t, m)
	assert.Error(t, m.Scan(42))
	nilValue, err := Map(nil).Value()
	require.NoError(t, err)
	assert.Nil(t, nilValue)
	assert.Equal(t, "jsonb", Map{}.GormDataType())
}

type widget struct {
	NamespaceModelIDString
	Name  string
	Attrs Map
}

type part struct {
	VersionedNamespaceModelIDInt
	Name string
}

type counter struct {
	NamespaceModelIDInt
	Name string
}

type doc struct {
	VersionedNamespaceModelIDString
	Name string
}

func ctxFor(ns, user string) context.Context {
	return utility.WithUser(utility.WithNamespace(context.Background(), ns), user)
}

func TestHooksFillAuditColumnsAndVersions(t *testing.T) {
	db, _ := testdb.Open(t)
	require.NoError(t, db.AutoMigrate(&widget{}, &part{}, &counter{}, &doc{}))
	ctx := ctxFor("ns-1", "creator")

	w := widget{Name: "w", Attrs: Map{"k": "v"}}
	require.NoError(t, db.WithContext(ctx).Create(&w).Error)
	assert.Len(t, w.ID, 36)
	assert.Equal(t, "ns-1", w.Namespace)
	assert.Equal(t, "creator", w.CreatedBy)
	assert.True(t, *w.IsActive)

	var loaded widget
	require.NoError(t, db.First(&loaded, "id = ?", w.ID).Error)
	assert.Equal(t, Map{"k": "v"}, loaded.Attrs)

	p := part{Name: "p"}
	require.NoError(t, db.WithContext(ctx).Create(&p).Error)
	assert.Equal(t, 1, p.Version)
	assert.NotZero(t, p.ID)
	p.Name = "p2"
	require.NoError(t, db.WithContext(ctxFor("ns-1", "editor")).Save(&p).Error)
	assert.Equal(t, 2, p.Version)
	assert.Equal(t, "editor", p.UpdatedBy)

	c := counter{Name: "c"}
	require.NoError(t, db.WithContext(ctx).Create(&c).Error)
	c.Name = "c2"
	require.NoError(t, db.WithContext(ctxFor("ns-1", "editor")).Save(&c).Error)
	assert.Equal(t, "editor", c.UpdatedBy)

	d := doc{Name: "d"}
	require.NoError(t, db.WithContext(ctx).Create(&d).Error)
	assert.Len(t, d.ID, 36)
	assert.Equal(t, 1, d.Version)
	require.NoError(t, db.WithContext(ctxFor("ns-1", "editor")).Save(&d).Error)
	assert.Equal(t, 2, d.Version)

	w.Name = "w2"
	require.NoError(t, db.WithContext(ctxFor("ns-1", "editor")).Save(&w).Error)
	assert.Equal(t, "editor", w.UpdatedBy)
}

func TestHooksRefuseWithoutNamespaceOrUser(t *testing.T) {
	db, _ := testdb.Open(t)
	require.NoError(t, db.AutoMigrate(&widget{}))
	err := db.Create(&widget{Name: "w"}).Error
	assert.Equal(t, errors.RequiredHeaderMissingErrorCode, errors.CodeOf(err))
	err = db.WithContext(utility.WithNamespace(context.Background(), "ns")).Create(&widget{Name: "w"}).Error
	assert.Equal(t, errors.RequiredHeaderMissingErrorCode, errors.CodeOf(err))

	w := widget{Name: "w"}
	require.NoError(t, db.WithContext(ctxFor("ns", "u")).Create(&w).Error)
	err = db.Save(&w).Error
	assert.Equal(t, errors.RequiredHeaderMissingErrorCode, errors.CodeOf(err))
}
