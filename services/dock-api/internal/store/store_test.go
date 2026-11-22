package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// open clones the seeded database named by TEST_DATABASE_URL (postgres://.../db) for one test and drops it after.
// Skips without TEST_DATABASE_URL. Seed it with `python -m setuhaul_jobs.seed --apply-schema`.
func open(t *testing.T) *Store {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(raw)
	require.NoError(t, err)
	source := strings.TrimPrefix(u.Path, "/")
	clone := fmt.Sprintf("%s_go_%d", source, time.Now().UnixNano())
	admin := *u
	admin.Path = "/postgres"
	adminDB, err := gorm.Open(postgres.Open(admin.String()), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, adminDB.Exec(fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", clone, source)).Error)
	cloned := *u
	cloned.Path = "/" + clone
	db, err := gorm.Open(postgres.Open(cloned.String()), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		adminDB.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", clone))
		if sqlDB, err := adminDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	// Start from the snapshot alone: no runs left by other test suites.
	require.NoError(t, db.Exec("TRUNCATE solver_run CASCADE").Error)
	s := New(db, DefaultSettings())
	setClock(t, s, "2026-08-04T09:55:00+05:30")
	return s
}

func setClock(t *testing.T, s *Store, at string) {
	t.Helper()
	db := s.GetDB(context.Background())
	require.NoError(t, db.Exec("DELETE FROM app_clock").Error)
	require.NoError(t, db.Exec("INSERT INTO app_clock (sim_anchor, real_anchor) VALUES (?::timestamptz, now())", at).Error)
}

func exec(t *testing.T, s *Store, sql string, args ...interface{}) {
	t.Helper()
	require.NoError(t, s.GetDB(context.Background()).Exec(sql, args...).Error)
}

func code(err error) string { return errors.CodeOf(err) }

func TestClockAndFacility(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now, err := s.Now(ctx)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Date(2026, 8, 4, 4, 25, 0, 0, time.UTC), now, time.Minute)
	ok, err := s.FacilityExists(ctx, "FAC-JAI-01")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = s.FacilityExists(ctx, "NOPE")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestBoardBeforeAnyRun(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	b, err := s.Board(ctx, "FAC-JAI-01")
	require.NoError(t, err)
	assert.Len(t, b.Docks, 6)
	assert.Nil(t, b.LastCommitted)
	assert.NotEmpty(t, b.Bookings)
	assert.NotEmpty(t, b.Blocks, "the D3 breakdown and the D2 capacity reduction are blocks")
	for _, bk := range b.Bookings {
		assert.Contains(t, []string{"PENDING_CONFIRMATION", "CONFIRMED", "IN_PROGRESS"}, bk.Status)
		assert.True(t, bk.SlotEnd.After(b.WindowStart) && bk.SlotStart.Before(b.WindowEnd))
		if bk.Status == "PENDING_CONFIRMATION" {
			require.NotNil(t, bk.HoldExpiresAt)
			assert.Equal(t, bk.BookedAt.Add(15*time.Minute), *bk.HoldExpiresAt)
		}
	}
	ex, err := s.Exceptions(ctx, "FAC-JAI-01")
	require.NoError(t, err)
	assert.Empty(t, ex)
	ch, err := s.Changes(ctx, "FAC-JAI-01", 10)
	require.NoError(t, err)
	assert.Empty(t, ch)
	runs, err := s.Runs(ctx, "FAC-JAI-01", 10)
	require.NoError(t, err)
	assert.Empty(t, runs)
}

func TestOptionsFollowTheSolverRules(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now, _ := s.Now(ctx)
	// SHP1010 needs a reefer dock; SHP1011 and SHP1016 are heavy loads.
	for shipment, dockType := range map[string]string{"SHP1010": "REEFER", "SHP1011": "HEAVY", "SHP1016": "HEAVY"} {
		opts, err := s.Options(ctx, shipment, 50)
		require.NoError(t, err, shipment)
		require.NotEmpty(t, opts, shipment)
		for _, o := range opts {
			assert.Equal(t, dockType, o.DockType, shipment)
			assert.False(t, o.SlotStart.Before(now), shipment)
		}
	}
	// SHP1018 cannot start before its ETA plus the 30-minute gate buffer.
	var eta time.Time
	require.NoError(t, s.GetDB(ctx).Raw("SELECT eta_ts FROM effective_eta('SHP1018', setuhaul_now())").Scan(&eta).Error)
	opts, err := s.Options(ctx, "SHP1018", 50)
	require.NoError(t, err)
	require.NotEmpty(t, opts)
	for _, o := range opts {
		assert.False(t, o.SlotStart.Before(eta.Add(30*time.Minute)), o.SlotID)
	}
	// D3 is broken down until 13:00: no option on it before then.
	for _, o := range mustOptions(t, s, "SHP1018") {
		if o.DockID == "DOCK-JAI-D3" {
			assert.False(t, o.SlotStart.Before(time.Date(2026, 8, 4, 7, 30, 0, 0, time.UTC)), o.SlotID)
		}
	}
	_, err = s.Options(ctx, "SHP-NOPE", 5)
	assert.Equal(t, errors.RecordNotFoundErrorCode, code(err))
}

func TestSideHoldThenConfirmReplacesTheBooking(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	opts, err := s.Options(ctx, "SHP1009", 2)
	require.NoError(t, err)
	require.Len(t, opts, 2)

	h, err := s.CreateHold(ctx, "SHP1009", opts[0].SlotID)
	require.NoError(t, err)
	assert.Equal(t, "PENDING_CONFIRMATION", h.Status)
	assert.False(t, h.IsCurrent, "the truck keeps its booking until the driver confirms")
	require.NotNil(t, h.ReplacesID)
	assert.Equal(t, h.BookedAt.Add(15*time.Minute), h.ExpiresAt)

	_, err = s.CreateHold(ctx, "SHP1009", opts[1].SlotID)
	assert.Equal(t, HoldConflictCode, code(err), "one hold per truck")
	_, err = s.CreateHold(ctx, "SHP1020", opts[0].SlotID)
	assert.Equal(t, SlotUnavailableCode, code(err), "a held slot is taken")
	again, err := s.Options(ctx, "SHP1020", 50)
	require.NoError(t, err)
	for _, o := range again {
		assert.NotEqual(t, opts[0].SlotID, o.SlotID)
	}

	c, err := s.ConfirmHold(ctx, h.AppointmentID)
	require.NoError(t, err)
	assert.Equal(t, "CONFIRMED", c.Status)
	assert.True(t, c.IsCurrent)
	var old struct {
		AppointmentStatus string
		IsCurrent         int
	}
	require.NoError(t, s.GetDB(ctx).Raw("SELECT appointment_status, is_current FROM appointments WHERE appointment_id = ?",
		*h.ReplacesID).Scan(&old).Error)
	assert.Equal(t, "CANCELLED", old.AppointmentStatus)
	assert.Equal(t, 0, old.IsCurrent)

	_, err = s.ConfirmHold(ctx, h.AppointmentID)
	assert.Equal(t, HoldConflictCode, code(err))
	_, err = s.ReleaseHold(ctx, h.AppointmentID)
	assert.Equal(t, HoldConflictCode, code(err))
	_, err = s.GetHold(ctx, "APT-NOPE")
	assert.Equal(t, errors.RecordNotFoundErrorCode, code(err))
}

func TestAnUnbookedTruckGetsACurrentHold(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	// SHP1018's only booking was a no-show: it has no current booking.
	opts, err := s.Options(ctx, "SHP1018", 1)
	require.NoError(t, err)
	require.NotEmpty(t, opts)
	h, err := s.CreateHold(ctx, "SHP1018", opts[0].SlotID)
	require.NoError(t, err)
	assert.True(t, h.IsCurrent)
	assert.Nil(t, h.ReplacesID)
	r, err := s.ReleaseHold(ctx, h.AppointmentID)
	require.NoError(t, err)
	assert.Equal(t, "CANCELLED", r.Status)
	assert.False(t, r.IsCurrent)
	_, err = s.CreateHold(ctx, "", "x")
	assert.Equal(t, errors.BadRequestErrorCode, code(err))
}

func TestExpiredHoldsCannotBeConfirmedAndAreSwept(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	opts, err := s.Options(ctx, "SHP1009", 1)
	require.NoError(t, err)
	h, err := s.CreateHold(ctx, "SHP1009", opts[0].SlotID)
	require.NoError(t, err)

	setClock(t, s, "2026-08-04T10:11:00+05:30")
	_, err = s.ConfirmHold(ctx, h.AppointmentID)
	assert.Equal(t, HoldExpiredCode, code(err))

	n, err := s.ExpireHolds(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, 2, "this hold and the snapshot's 09:52 hold of SHP1014")
	got, err := s.GetHold(ctx, h.AppointmentID)
	require.NoError(t, err)
	assert.Equal(t, "CANCELLED", got.Status)
	n, err = s.ExpireHolds(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestConcurrentHoldsOnOneSlotOneWins(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	// Trucks without a pending hold that can all use the same standard slot.
	trucks := []string{"SHP1003", "SHP1004", "SHP1006", "SHP1007", "SHP1012", "SHP1017", "SHP1018", "SHP1020"}
	var slot string
	for _, o := range mustOptions(t, s, "SHP1018") {
		usable := 0
		for _, tr := range trucks {
			for _, p := range mustOptions(t, s, tr) {
				if p.SlotID == o.SlotID {
					usable++
				}
			}
		}
		if usable >= 4 {
			slot = o.SlotID
			break
		}
	}
	require.NotEmpty(t, slot, "a slot several trucks can use")
	var wg sync.WaitGroup
	results := make(chan error, len(trucks))
	for _, tr := range trucks {
		wg.Add(1)
		go func(tr string) {
			defer wg.Done()
			_, err := s.CreateHold(ctx, tr, slot)
			results <- err
		}(tr)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else {
			assert.Equal(t, SlotUnavailableCode, code(err))
		}
	}
	assert.Equal(t, 1, wins)
	var active int64
	require.NoError(t, s.GetDB(ctx).Raw(`SELECT count(*) FROM appointments WHERE slot_id = ?
		AND appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS')`, slot).Scan(&active).Error)
	assert.EqualValues(t, 1, active)
}

func mustOptions(t *testing.T, s *Store, shipment string) []Option {
	t.Helper()
	o, err := s.Options(context.Background(), shipment, 50)
	require.NoError(t, err)
	return o
}

func TestQueueTrucks(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for _, n := range []int{0, 51} {
		_, err := s.QueueTrucks(ctx, "FAC-JAI-01", n)
		assert.Equal(t, errors.BadRequestErrorCode, code(err), n)
	}
	job, err := s.QueueTrucks(ctx, "FAC-JAI-01", 10)
	require.NoError(t, err)
	assert.Equal(t, "PENDING", job.Status)
	assert.Equal(t, 10, job.Trucks)
	got, err := s.RequestJob(ctx, job.JobID)
	require.NoError(t, err)
	assert.Equal(t, job.JobID, got.JobID)
	_, err = s.RequestJob(ctx, job.JobID+1000)
	assert.Equal(t, errors.RecordNotFoundErrorCode, code(err))
}

func TestARequestNotMadeYetHasNoOptions(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	// SHP1018 becomes a request made at 11:00: before then it does not exist for drivers or holds.
	exec(t, s, "UPDATE shipments SET created_at = '2026-08-04T11:00:00+05:30' WHERE shipment_id = 'SHP1018'")
	_, err := s.Options(ctx, "SHP1018", 5)
	assert.Equal(t, errors.RecordNotFoundErrorCode, code(err))
	setClock(t, s, "2026-08-04T11:01:00+05:30")
	opts, err := s.Options(ctx, "SHP1018", 5)
	require.NoError(t, err)
	assert.NotEmpty(t, opts)
}
