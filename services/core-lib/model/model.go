// Package model holds the base models every table embeds: id, namespace, is_active, the audit columns,
// optional optimistic-lock version and soft delete. GORM hooks fill namespace and the audit users from the
// request context, so modules never set them by hand.
package model

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Map is a JSONB column.
type Map map[string]interface{}

func (m Map) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

// Scan replaces the map's contents (never merges into a map already held by a reused struct).
func (m *Map) Scan(src interface{}) error {
	var raw []byte
	switch v := src.(type) {
	case nil:
		*m = nil
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("model.Map: cannot scan %T", src)
	}
	fresh := Map{}
	if err := json.Unmarshal(raw, &fresh); err != nil {
		return err
	}
	*m = fresh
	return nil
}

// GormDataType makes AutoMigrate and GORM treat Map as jsonb.
func (Map) GormDataType() string { return "jsonb" }

// Audit holds the columns every base model shares.
type Audit struct {
	Namespace string         `gorm:"column:namespace"`
	IsActive  *bool          `gorm:"column:is_active;default:true"`
	CreatedBy string         `gorm:"column:created_by"`
	UpdatedBy string         `gorm:"column:updated_by"`
	DeletedBy *string        `gorm:"column:deleted_by"`
	CreatedAt time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time      `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

func ctxOf(tx *gorm.DB) context.Context {
	if tx != nil && tx.Statement != nil && tx.Statement.Context != nil {
		return tx.Statement.Context
	}
	return context.Background()
}

// beforeCreate fills namespace, created_by and is_active from the request context.
func (a *Audit) beforeCreate(tx *gorm.DB) error {
	ctx := ctxOf(tx)
	if a.Namespace == "" {
		ns, err := utility.GetNamespaceID(ctx)
		if err != nil {
			return err
		}
		a.Namespace = ns
	}
	if a.CreatedBy == "" {
		user, err := utility.GetUserID(ctx)
		if err != nil {
			return err
		}
		a.CreatedBy = user
	}
	if a.IsActive == nil {
		a.IsActive = utility.GetPointerToPrimitiveType(true)
	}
	return nil
}

// beforeUpdate records who changed the row.
func (a *Audit) beforeUpdate(tx *gorm.DB) error {
	user, err := utility.GetUserID(ctxOf(tx))
	if err != nil {
		return err
	}
	a.UpdatedBy = user
	return nil
}

// NamespaceModelIDInt is the base for tables with a serial id.
type NamespaceModelIDInt struct {
	ID int `gorm:"column:id;primaryKey;autoIncrement"`
	Audit
}

func (m *NamespaceModelIDInt) BeforeCreate(tx *gorm.DB) error { return m.beforeCreate(tx) }
func (m *NamespaceModelIDInt) BeforeUpdate(tx *gorm.DB) error { return m.beforeUpdate(tx) }

// NamespaceModelIDString is the base for tables with a varchar(64) UUID id set in Go.
type NamespaceModelIDString struct {
	ID string `gorm:"column:id;primaryKey"`
	Audit
}

func (m *NamespaceModelIDString) BeforeCreate(tx *gorm.DB) error {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	return m.beforeCreate(tx)
}
func (m *NamespaceModelIDString) BeforeUpdate(tx *gorm.DB) error { return m.beforeUpdate(tx) }

// VersionedNamespaceModelIDInt adds optimistic locking: an update must name the version it read, and moves it on by one.
type VersionedNamespaceModelIDInt struct {
	ID      int `gorm:"column:id;primaryKey;autoIncrement"`
	Version int `gorm:"column:version;default:1"`
	Audit
}

func (m *VersionedNamespaceModelIDInt) BeforeCreate(tx *gorm.DB) error {
	if m.Version == 0 {
		m.Version = 1
	}
	return m.beforeCreate(tx)
}
func (m *VersionedNamespaceModelIDInt) BeforeUpdate(tx *gorm.DB) error {
	m.Version++
	return m.beforeUpdate(tx)
}

// VersionedNamespaceModelIDString is VersionedNamespaceModelIDInt with a UUID id.
type VersionedNamespaceModelIDString struct {
	ID      string `gorm:"column:id;primaryKey"`
	Version int    `gorm:"column:version;default:1"`
	Audit
}

func (m *VersionedNamespaceModelIDString) BeforeCreate(tx *gorm.DB) error {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if m.Version == 0 {
		m.Version = 1
	}
	return m.beforeCreate(tx)
}
func (m *VersionedNamespaceModelIDString) BeforeUpdate(tx *gorm.DB) error {
	m.Version++
	return m.beforeUpdate(tx)
}
