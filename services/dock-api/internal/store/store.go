// Package store reads the plan view's data and writes driver holds. Every time comes from setuhaul_now(), the
// shared clock, so the API, the solver and the GPS feed agree on what "now" is.
package store

import (
	"context"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/transaction"
	"gorm.io/gorm"
)

// Settings are the planning rules the hold options must respect. They mirror the solver's settings and must be
// configured to the same values (gate buffer, low-confidence margin, horizon, hold expiry).
type Settings struct {
	GateBufferMin          int
	LowConfidenceMarginMin int
	HorizonHours           int
	HoldExpiryMin          int
}

// DefaultSettings are the plan's defaults.
func DefaultSettings() Settings {
	return Settings{GateBufferMin: 30, LowConfidenceMarginMin: 15, HorizonHours: 12, HoldExpiryMin: 15}
}

// Store wraps the database.
type Store struct {
	*transaction.GormExecutor
	settings Settings
}

// New returns a store on db.
func New(db *gorm.DB, s Settings) *Store {
	return &Store{GormExecutor: transaction.NewGormExecutor(db), settings: s}
}

// Settings returns the rules in force.
func (s *Store) Settings() Settings { return s.settings }

// Now is the shared clock.
func (s *Store) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := s.GetDB(ctx).Raw("SELECT setuhaul_now()").Scan(&now).Error
	return now, errors.RepositoryError(err)
}

// lockFacility serialises booking writes per facility with the solver's commit (same advisory lock key).
func lockFacility(db *gorm.DB, facilityID string) error {
	return db.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "setuhaul.bookings:"+facilityID).Error
}

// FacilityExists reports whether the facility is known.
func (s *Store) FacilityExists(ctx context.Context, facilityID string) (bool, error) {
	var n int64
	err := s.GetDB(ctx).Raw("SELECT count(*) FROM facilities WHERE facility_id = ?", facilityID).Scan(&n).Error
	return n > 0, errors.RepositoryError(err)
}
