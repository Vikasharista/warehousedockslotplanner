package store

import (
	"context"
	"testing"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentShipmentStatusAndMessages(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	// SHP1009 has a CONFIRMED booking in the snapshot.
	a, err := s.AgentShipment(ctx, "SHP1009")
	require.NoError(t, err)
	require.NotNil(t, a.Booking)
	assert.Equal(t, "CONFIRMED", a.Booking.Status)
	assert.Contains(t, a.Message, "Your slot is dock "+a.Booking.DockCode)
	require.NotNil(t, a.Eta)

	// A hold beside the booking shows as the hold, with its expiry.
	opts, err := s.Options(ctx, "SHP1009", 1)
	require.NoError(t, err)
	_, err = s.CreateHold(ctx, "SHP1009", opts[0].SlotID)
	require.NoError(t, err)
	a, err = s.AgentShipment(ctx, "SHP1009")
	require.NoError(t, err)
	require.NotNil(t, a.Hold)
	assert.Contains(t, a.Message, "You are holding dock")

	// SHP1018 has no current booking and no plan yet.
	a, err = s.AgentShipment(ctx, "SHP1018")
	require.NoError(t, err)
	assert.Nil(t, a.Booking)
	assert.Equal(t, "Your request is being planned; you will get a slot shortly.", a.Message)

	_, err = s.AgentShipment(ctx, "SHP-NOPE")
	assert.Equal(t, errors.RecordNotFoundErrorCode, code(err))
}

func TestShipmentsByPhoneMatchesTheLastTenDigits(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	var phone string
	require.NoError(t, s.GetDB(ctx).Raw(`SELECT d.phone FROM shipments s JOIN drivers d USING (driver_id)
		WHERE s.shipment_id = 'SHP1009'`).Scan(&phone).Error)
	digits := nonDigits.ReplaceAllString(phone, "")
	list, err := s.ShipmentsByPhone(ctx, "0"+digits[len(digits)-10:])
	require.NoError(t, err)
	require.NotEmpty(t, list)
	assert.Equal(t, phone, list[0].DriverPhone)
	_, err = s.ShipmentsByPhone(ctx, "12345")
	assert.Equal(t, errors.BadRequestErrorCode, code(err))
	_, err = s.ShipmentsByPhone(ctx, "+1 555 000 0000")
	assert.Equal(t, errors.RecordNotFoundErrorCode, code(err))
}

func TestReportETA(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now, _ := s.Now(ctx)
	facility, err := s.ReportETA(ctx, "SHP1009", now.Add(3*time.Hour), SourceDriver, "", "TRAFFIC", "stuck near Shahpura")
	require.NoError(t, err)
	assert.Equal(t, "FAC-JAI-01", facility)
	var eta time.Time
	require.NoError(t, s.GetDB(ctx).Raw("SELECT eta_ts FROM effective_eta('SHP1009', setuhaul_now())").Scan(&eta).Error)
	if a, _ := s.AgentShipment(ctx, "SHP1009"); a.EtaSource != nil && *a.EtaSource == "DRIVER_DECLARED" {
		assert.WithinDuration(t, now.Add(3*time.Hour), eta, time.Second)
	}
	for name, call := range map[string]func() error{
		"source":     func() error { _, e := s.ReportETA(ctx, "SHP1009", now, "GUESS", "", "", ""); return e },
		"confidence": func() error { _, e := s.ReportETA(ctx, "SHP1009", now, SourceDriver, "SURE", "", ""); return e },
		"reason":     func() error { _, e := s.ReportETA(ctx, "SHP1009", now, SourceDriver, "", "ALIENS", ""); return e },
		"too far": func() error {
			_, e := s.ReportETA(ctx, "SHP1009", now.Add(72*time.Hour), SourceDriver, "", "", "")
			return e
		},
		"past": func() error {
			_, e := s.ReportETA(ctx, "SHP1009", now.Add(-2*time.Hour), SourceDriver, "", "", "")
			return e
		},
	} {
		assert.Equal(t, errors.BadRequestErrorCode, code(call()), name)
	}
	_, err = s.ReportETA(ctx, "SHP-NOPE", now, SourceDriver, "", "", "")
	assert.Equal(t, errors.RecordNotFoundErrorCode, code(err))
}

func TestChangesFeedIsOrderedFromTheCursor(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	exec(t, s, `INSERT INTO solver_run (run_id, facility_id, as_of_ts, horizon_end_ts, run_trigger, status, score,
		feasible, trucks_planned, assigned, unassigned, avg_wait_min, weighted_wait_min, priority_inversions,
		pickup_inversions, forced_moves, unforced_moves, dock_utilisation_pct, settings, solve_ms, committed_at)
		VALUES ('RUN-F', 'FAC-JAI-01', setuhaul_now(), setuhaul_now() + interval '12 hours', 'MANUAL', 'COMMITTED', 's',
		        true, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, '{}', 0, setuhaul_now())`)
	exec(t, s, `INSERT INTO booking_changes (facility_id, run_id, shipment_id, action, from_slot_id, to_slot_id,
		reason_code, changed_at) VALUES
		('FAC-JAI-01', 'RUN-F', 'SHP1009', 'MOVED', 'SLOT-JAI-046', 'SLOT-JAI-049', 'TRUCK_CANNOT_MAKE_SLOT', setuhaul_now()),
		('FAC-JAI-01', 'RUN-F', 'SHP1018', 'NEW', NULL, 'SLOT-JAI-021', NULL, setuhaul_now()),
		('FAC-JAI-01', 'RUN-F', 'SHP1020', 'RELEASED', 'SLOT-JAI-025', NULL, 'BUMPED_FOR_CRITICAL', setuhaul_now())`)
	all, last, err := s.ChangesAfter(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, all[2].ChangeID, last)
	assert.Contains(t, all[0].Message, "SHP1009 moved from dock D4")
	assert.Contains(t, all[0].Message, "your new ETA is later than the slot")
	assert.Contains(t, all[1].Message, "SHP1018 is booked on dock")
	assert.Contains(t, all[2].Message, "an urgent delivery needed that slot")
	rest, _, err := s.ChangesAfter(ctx, all[0].ChangeID, 10)
	require.NoError(t, err)
	assert.Len(t, rest, 2)
	page, last, err := s.ChangesAfter(ctx, 0, 1)
	require.NoError(t, err)
	assert.Len(t, page, 1)
	assert.Equal(t, all[2].ChangeID, last, "last is the newest id, whatever the page")
	past, last, err := s.ChangesAfter(ctx, all[2].ChangeID+100, 10)
	require.NoError(t, err)
	assert.Empty(t, past)
	assert.Equal(t, all[2].ChangeID, last, "a cursor above last shows the feed was reset")
}

func TestShipmentsCreateEditAndCheckIn(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	before, err := s.Shipments(ctx, "FAC-JAI-01")
	require.NoError(t, err)
	assert.Len(t, before, 17, "the snapshot's open shipments to Jaipur")

	now, _ := s.Now(ctx)
	created, err := s.CreateShipment(ctx, "FAC-JAI-01", NewShipment{PriorityCode: "critical", RequiredDockType: "",
		LoadWeightKg: 12000, UnloadMin: 45, OriginCity: "Ajmer", Eta: now.Add(2 * time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "SHP-M0001", created.ShipmentID)
	after, _ := s.Shipments(ctx, "FAC-JAI-01")
	assert.Len(t, after, len(before)+1)
	assert.Equal(t, "CRITICAL", created.PriorityCode)
	assert.Equal(t, "ANY", created.RequiredDockType)
	var vtype string
	require.NoError(t, s.GetDB(ctx).Raw(`SELECT v.vehicle_type_code FROM shipments s JOIN vehicles v USING (vehicle_id)
		WHERE s.shipment_id = 'SHP-M0001'`).Scan(&vtype).Error)
	assert.Equal(t, "32FT_SXL", vtype)
	reefer, err := s.CreateShipment(ctx, "FAC-JAI-01", NewShipment{PriorityCode: "HIGH", RequiredDockType: "REEFER",
		LoadWeightKg: 9000, UnloadMin: 60, OriginCity: "Delhi", Eta: now.Add(4 * time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "SHP-M0002", reefer.ShipmentID)

	for name, bad := range map[string]NewShipment{
		"priority": {PriorityCode: "URGENT", LoadWeightKg: 1000, UnloadMin: 30, OriginCity: "X", Eta: now.Add(time.Hour)},
		"weight":   {PriorityCode: "LOW", LoadWeightKg: 40000, UnloadMin: 30, OriginCity: "X", Eta: now.Add(time.Hour)},
		"origin":   {PriorityCode: "LOW", LoadWeightKg: 1000, UnloadMin: 30, Eta: now.Add(time.Hour)},
		"eta":      {PriorityCode: "LOW", LoadWeightKg: 1000, UnloadMin: 30, OriginCity: "X", Eta: now.Add(-time.Hour)},
		"reefer":   {PriorityCode: "LOW", RequiredDockType: "REEFER", LoadWeightKg: 20000, UnloadMin: 30, OriginCity: "X", Eta: now.Add(time.Hour)},
	} {
		_, err := s.CreateShipment(ctx, "FAC-JAI-01", bad)
		assert.Equal(t, errors.BadRequestErrorCode, code(err), name)
	}

	facility, err := s.UpdateShipment(ctx, "SHP-M0001", ShipmentEdit{PriorityCode: "low", UnloadMin: 90})
	require.NoError(t, err)
	assert.Equal(t, "FAC-JAI-01", facility)
	var got struct {
		PriorityCode      string
		ExpectedUnloadMin int
		LoadWeightKg      int
	}
	require.NoError(t, s.GetDB(ctx).Raw(`SELECT priority_code, expected_unload_min, load_weight_kg FROM shipments
		WHERE shipment_id = 'SHP-M0001'`).Scan(&got).Error)
	assert.Equal(t, "LOW", got.PriorityCode)
	assert.Equal(t, 90, got.ExpectedUnloadMin)
	assert.Equal(t, 12000, got.LoadWeightKg)
	_, err = s.UpdateShipment(ctx, "SHP-M0001", ShipmentEdit{UnloadMin: 999})
	assert.Equal(t, errors.BadRequestErrorCode, code(err))

	require.NoError(t, s.CheckIn(ctx, "SHP-M0001"))
	assert.Equal(t, HoldConflictCode, code(s.CheckIn(ctx, "SHP-M0001")))
	a, err := s.AgentShipment(ctx, "SHP-M0001")
	require.NoError(t, err)
	require.NotNil(t, a.GateIn)
	assert.Contains(t, a.Message, "checked in at the gate")
	var state string
	require.NoError(t, s.GetDB(ctx).Raw("SELECT arrival_state FROM facility_checkins WHERE shipment_id = 'SHP-M0001'").
		Scan(&state).Error)
	assert.Equal(t, "EARLY", state, "two hours before its ETA")
}

func TestCapacityChanges(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	c, err := s.CapacityView(ctx, "FAC-JAI-01")
	require.NoError(t, err)
	assert.Len(t, c.Docks, 6)
	assert.NotEmpty(t, c.Slots)
	eventsBefore := len(c.Events)

	now, _ := s.Now(ctx)
	end := now.Add(3 * time.Hour)
	code1, err := s.AddDockEvent(ctx, "FAC-JAI-01", NewDockEvent{DockID: "DOCK-JAI-D4", Type: "breakdown",
		Start: now.Add(time.Hour), End: &end, Reason: "leveller fault"})
	require.NoError(t, err)
	assert.Equal(t, "D4", code1)
	c, _ = s.CapacityView(ctx, "FAC-JAI-01")
	assert.Len(t, c.Events, eventsBefore+1)
	for name, bad := range map[string]NewDockEvent{
		"type":   {DockID: "DOCK-JAI-D4", Type: "PARTY", Start: now, Reason: "x"},
		"reason": {DockID: "DOCK-JAI-D4", Type: "MAINTENANCE", Start: now},
		"order":  {DockID: "DOCK-JAI-D4", Type: "MAINTENANCE", Start: now, End: &now, Reason: "x"},
	} {
		_, err := s.AddDockEvent(ctx, "FAC-JAI-01", bad)
		assert.Equal(t, errors.BadRequestErrorCode, code(err), name)
	}
	_, err = s.AddDockEvent(ctx, "FAC-JAI-01", NewDockEvent{DockID: "DOCK-NOPE", Type: "MAINTENANCE", Start: now, Reason: "x"})
	assert.Equal(t, errors.RecordNotFoundErrorCode, code(err))

	slot := c.Slots[len(c.Slots)-1].SlotID
	facility, label, err := s.SetSlotStatus(ctx, slot, "blocked", "forklift service")
	require.NoError(t, err)
	assert.Equal(t, "FAC-JAI-01", facility)
	assert.NotEmpty(t, label)
	_, _, err = s.SetSlotStatus(ctx, slot, "BLOCKED", "")
	assert.Equal(t, errors.BadRequestErrorCode, code(err))
	_, _, err = s.SetSlotStatus(ctx, slot, "OPEN", "")
	require.NoError(t, err)
	_, _, err = s.SetSlotStatus(ctx, "SLOT-NOPE", "OPEN", "")
	assert.Equal(t, errors.RecordNotFoundErrorCode, code(err))

	d, err := s.SetDockStatus(ctx, "FAC-JAI-01", "DOCK-JAI-D1", "out_of_service")
	require.NoError(t, err)
	assert.Equal(t, "D1", d)
	opts, err := s.Options(ctx, "SHP1018", 50)
	require.NoError(t, err)
	for _, o := range opts {
		assert.NotEqual(t, "DOCK-JAI-D1", o.DockID, "a dock out of service offers no slots")
	}
	_, err = s.SetDockStatus(ctx, "FAC-JAI-01", "DOCK-JAI-D1", "PARTY")
	assert.Equal(t, errors.BadRequestErrorCode, code(err))
}

// Database guide cases "Ambiguous shipment" (DRV004 has two trips) and "Cancelled shipment message" (SHP1019).
func TestGuideCasesAmbiguousDriverAndCancelledShipment(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	both, err := s.ShipmentsByPhone(ctx, "+91-9000010004")
	require.NoError(t, err)
	ids := []string{}
	for _, sh := range both {
		ids = append(ids, sh.ShipmentID)
	}
	assert.ElementsMatch(t, []string{"SHP1004", "SHP1020"}, ids, "the agent sees both trips and must ask which one")

	// SHP1019's driver has no open trip; the cancelled shipment itself is not open for ETA reports.
	_, err = s.ShipmentsByPhone(ctx, "+91-9000010008")
	assert.Error(t, err)
	_, err = s.ReportETA(ctx, "SHP1019", time.Date(2026, 8, 4, 12, 0, 0, 0, IST), SourceDriver, "", "TRAFFIC", "")
	assert.Error(t, err)
}
