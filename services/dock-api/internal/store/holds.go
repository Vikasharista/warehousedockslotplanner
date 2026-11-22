package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Error codes beyond core-lib's.
const (
	// SlotUnavailableCode: the slot is taken, blocked, or not usable by this truck.
	SlotUnavailableCode = "SLOT_UNAVAILABLE"
	// HoldConflictCode: the hold is not in a state that allows the request.
	HoldConflictCode = "HOLD_CONFLICT"
	// HoldExpiredCode: the hold ran out before the driver confirmed.
	HoldExpiredCode = "HOLD_EXPIRED"
)

// Option is a slot a driver may hold.
type Option struct {
	SlotID    string    `json:"slotId"`
	DockID    string    `json:"dockId"`
	DockCode  string    `json:"dockCode"`
	DockType  string    `json:"dockType"`
	SlotStart time.Time `json:"slotStart"`
	SlotEnd   time.Time `json:"slotEnd"`
}

// optionsSQL lists the free slots a shipment can use now, by the solver's hard rules: OPEN slot on an active dock
// of the destination, no dock event overlapping the unload, not before the truck can be ready (gate-in, or
// effective ETA + gate buffer, + the low-confidence margin), not after the facility's last start, inside the
// horizon, the right dock type (reefer, heavy, explicit type) and weight, no active booking on it, and no other
// truck's booking or hold on the dock whose unload overlaps this truck's (a 65-minute unload in the 18:00 slot runs
// into 19:00; the solver's H7, the validator's V1).
const optionsSQL = `
WITH now AS (SELECT setuhaul_now() AS ts),
sh AS (
    SELECT s.shipment_id, s.destination_facility_id AS facility_id, s.load_weight_kg, s.required_dock_type,
           s.expected_unload_min,
           (s.temperature_control_required = 1 OR vt.refrigerated_flag = 1 OR s.required_dock_type = 'REEFER') AS reefer,
           fc.gate_in_ts, e.eta_ts, e.confidence, f.timezone
    FROM shipments s
    JOIN vehicles v ON v.vehicle_id = s.vehicle_id
    JOIN vehicle_types vt ON vt.vehicle_type_code = v.vehicle_type_code
    JOIN facilities f ON f.facility_id = s.destination_facility_id
    CROSS JOIN now
    LEFT JOIN facility_checkins fc ON fc.shipment_id = s.shipment_id AND fc.gate_in_ts <= now.ts
    LEFT JOIN LATERAL effective_eta(s.shipment_id, now.ts) e ON TRUE
    WHERE s.shipment_id = @shipment AND s.current_status NOT IN ('COMPLETED', 'CANCELLED')
      AND s.created_at <= now.ts
),
rules AS (
    SELECT max(rule_value) FILTER (WHERE rule_type = 'LAST_NEW_START_TIME') AS last_start,
           max(rule_value) FILTER (WHERE rule_type = 'HEAVY_DOCK_REQUIRED_KG') AS heavy_kg
    FROM facility_rules r JOIN sh ON sh.facility_id = r.facility_id
    CROSS JOIN now
    WHERE r.active_flag = 1
      AND (r.effective_from IS NULL OR r.effective_from <= (now.ts AT TIME ZONE sh.timezone)::date)
      AND (r.effective_to IS NULL OR r.effective_to >= (now.ts AT TIME ZONE sh.timezone)::date)
),
ready AS (
    SELECT sh.*, rules.*,
           GREATEST(now.ts, COALESCE(sh.gate_in_ts,
               sh.eta_ts + make_interval(mins => @buffer + CASE WHEN sh.confidence = 'LOW' THEN @margin ELSE 0 END)))
               AS ready_ts,
           now.ts AS now_ts
    FROM sh CROSS JOIN rules CROSS JOIN now
)
SELECT sl.slot_id, d.dock_id, d.dock_code, d.dock_type, sl.slot_start_ts AS slot_start, sl.slot_end_ts AS slot_end
FROM ready r
JOIN appointment_slots sl ON sl.facility_id = r.facility_id
JOIN docks d ON d.dock_id = sl.dock_id
WHERE sl.slot_status = 'OPEN' AND d.dock_status = 'ACTIVE'
  AND sl.slot_start_ts >= r.ready_ts
  AND sl.slot_start_ts < r.now_ts + make_interval(hours => @horizon)
  AND (r.last_start IS NULL OR (sl.slot_start_ts AT TIME ZONE r.timezone)::time <= r.last_start::time)
  AND r.load_weight_kg <= d.max_vehicle_weight_kg
  AND (NOT r.reefer OR (d.dock_type = 'REEFER' AND d.supports_refrigerated = 1))
  AND (CASE
         WHEN r.required_dock_type = 'HEAVY' OR (r.heavy_kg IS NOT NULL AND r.load_weight_kg > r.heavy_kg::int)
           THEN d.dock_type = 'HEAVY'
         WHEN r.required_dock_type = 'STANDARD' THEN d.dock_type = 'STANDARD'
         ELSE TRUE
       END)
  AND NOT EXISTS (SELECT 1 FROM appointments a WHERE a.slot_id = sl.slot_id
                  AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS'))
  AND NOT EXISTS (SELECT 1 FROM appointments a
                  JOIN appointment_slots osl ON osl.slot_id = a.slot_id
                  JOIN shipments os ON os.shipment_id = a.shipment_id
                  WHERE osl.dock_id = d.dock_id AND a.shipment_id <> r.shipment_id
                    AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS')
                    AND osl.slot_start_ts < sl.slot_start_ts + make_interval(mins => GREATEST(r.expected_unload_min,
                        (EXTRACT(EPOCH FROM sl.slot_end_ts - sl.slot_start_ts) / 60)::int))
                    AND sl.slot_start_ts < osl.slot_start_ts + make_interval(mins => GREATEST(os.expected_unload_min,
                        (EXTRACT(EPOCH FROM osl.slot_end_ts - osl.slot_start_ts) / 60)::int)))
  AND NOT EXISTS (SELECT 1 FROM dock_status_events ev
                  WHERE ev.dock_id = d.dock_id AND ev.created_at <= r.now_ts
                    AND ev.event_start_ts < sl.slot_start_ts + make_interval(mins => GREATEST(r.expected_unload_min,
                        (EXTRACT(EPOCH FROM sl.slot_end_ts - sl.slot_start_ts) / 60)::int))
                    AND (ev.event_end_ts IS NULL OR ev.event_end_ts > sl.slot_start_ts))
  AND (@slot = '' OR sl.slot_id = @slot)
ORDER BY sl.slot_start_ts, d.dock_code
LIMIT @limit`

// Options returns up to limit slots the shipment could hold now; RECORD_NOT_FOUND for an unknown or finished shipment.
func (s *Store) Options(ctx context.Context, shipmentID string, limit int) ([]Option, error) {
	if err := s.shipmentOpen(ctx, shipmentID); err != nil {
		return nil, err
	}
	return s.options(s.GetDB(ctx), shipmentID, "", limit)
}

func (s *Store) options(db *gorm.DB, shipmentID, slotID string, limit int) ([]Option, error) {
	out := []Option{}
	err := db.Raw(optionsSQL, map[string]interface{}{
		"shipment": shipmentID, "slot": slotID, "limit": limit, "buffer": s.settings.GateBufferMin,
		"margin": s.settings.LowConfidenceMarginMin, "horizon": s.settings.HorizonHours,
	}).Scan(&out).Error
	return out, errors.RepositoryError(err)
}

func (s *Store) shipmentOpen(ctx context.Context, shipmentID string) error {
	var n int64
	if err := s.GetDB(ctx).Raw(`SELECT count(*) FROM shipments WHERE shipment_id = ?
		AND current_status NOT IN ('COMPLETED', 'CANCELLED') AND created_at <= setuhaul_now()`, shipmentID).
		Scan(&n).Error; err != nil {
		return errors.RepositoryError(err)
	}
	if n == 0 {
		return errors.NewRecordNotFoundError("no open shipment " + shipmentID)
	}
	return nil
}

// Hold is a driver's PENDING_CONFIRMATION booking (or what became of it).
type Hold struct {
	AppointmentID string     `json:"appointmentId"`
	ShipmentID    string     `json:"shipmentId"`
	SlotID        string     `json:"slotId"`
	Status        string     `json:"status"`
	IsCurrent     bool       `json:"isCurrent"`
	ReplacesID    *string    `json:"replacesAppointmentId,omitempty"`
	BookedAt      time.Time  `json:"bookedAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	ConfirmedAt   *time.Time `json:"confirmedAt,omitempty"`
	CancelledAt   *time.Time `json:"cancelledAt,omitempty"`
	FacilityID    string     `json:"facilityId"`
}

func (s *Store) hold(db *gorm.DB, id string, forUpdate bool) (*Hold, error) {
	q := `SELECT a.appointment_id, a.shipment_id, a.slot_id, a.appointment_status AS status, a.is_current = 1 AS is_current,
	             a.replaced_appointment_id AS replaces_id, a.booked_at,
	             a.booked_at + make_interval(mins => ?) AS expires_at, a.confirmed_at, a.cancelled_at, sl.facility_id
	      FROM appointments a JOIN appointment_slots sl ON sl.slot_id = a.slot_id
	      WHERE a.appointment_id = ? AND a.booking_source = 'DRIVER_CHAT'`
	if forUpdate {
		q += " FOR UPDATE OF a"
	}
	var holds []Hold
	if err := db.Raw(q, s.settings.HoldExpiryMin, id).Scan(&holds).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(holds) == 0 {
		return nil, errors.NewRecordNotFoundError("no driver hold " + id)
	}
	return &holds[0], nil
}

// GetHold returns one hold.
func (s *Store) GetHold(ctx context.Context, id string) (*Hold, error) {
	return s.hold(s.GetDB(ctx), id, false)
}

// CreateHold puts a driver's hold on a slot. A truck without a current booking gets a current hold; a truck that
// has one keeps it, and the hold sits beside it (is_current = 0) until the driver confirms or it expires.
func (s *Store) CreateHold(ctx context.Context, shipmentID, slotID string) (*Hold, error) {
	if shipmentID == "" || slotID == "" {
		return nil, errors.NewBadRequestError("shipmentId and slotId are required")
	}
	if err := s.shipmentOpen(ctx, shipmentID); err != nil {
		return nil, err
	}
	var created *Hold
	err := s.DoInTransaction(ctx, func(ctx context.Context) error {
		db := s.GetDB(ctx)
		var facility string
		if err := db.Raw("SELECT destination_facility_id FROM shipments WHERE shipment_id = ?", shipmentID).
			Scan(&facility).Error; err != nil {
			return errors.RepositoryError(err)
		}
		if err := lockFacility(db, facility); err != nil {
			return errors.RepositoryError(err)
		}
		var pending []string
		if err := db.Raw(`SELECT appointment_id FROM appointments WHERE shipment_id = ?
			AND appointment_status = 'PENDING_CONFIRMATION'`, shipmentID).Scan(&pending).Error; err != nil {
			return errors.RepositoryError(err)
		}
		if len(pending) > 0 {
			return errors.New(HoldConflictCode, fmt.Sprintf("%s already holds %s: confirm or release it first",
				shipmentID, strings.Join(pending, ", ")))
		}
		ok, err := s.options(db, shipmentID, slotID, 1)
		if err != nil {
			return err
		}
		if len(ok) == 0 {
			return errors.New(SlotUnavailableCode, fmt.Sprintf("slot %s is not available to %s", slotID, shipmentID))
		}
		var current []struct {
			AppointmentID string
			Status        string
		}
		if err := db.Raw(`SELECT appointment_id, appointment_status AS status FROM appointments
			WHERE shipment_id = ? AND is_current = 1 AND appointment_status IN ('CONFIRMED', 'IN_PROGRESS')`,
			shipmentID).Scan(&current).Error; err != nil {
			return errors.RepositoryError(err)
		}
		isCurrent, replaces := 1, (*string)(nil)
		if len(current) > 0 {
			if current[0].Status == "IN_PROGRESS" {
				return errors.New(HoldConflictCode, shipmentID+" is already unloading")
			}
			isCurrent, replaces = 0, &current[0].AppointmentID
		}
		id := "APT-H-" + strings.ToUpper(uuid.NewString()[:13])
		if err := db.Exec(`INSERT INTO appointments (appointment_id, shipment_id, slot_id, appointment_status,
				booking_source, is_current, booked_at, replaced_appointment_id, updated_at)
			VALUES (?, ?, ?, 'PENDING_CONFIRMATION', 'DRIVER_CHAT', ?, setuhaul_now(), ?, setuhaul_now())`,
			id, shipmentID, slotID, isCurrent, replaces).Error; err != nil {
			if errors.CodeOf(errors.RepositoryError(err)) == errors.DuplicateRequestErrorCode {
				return errors.Wrap(SlotUnavailableCode, "slot "+slotID+" was just taken", err)
			}
			return errors.RepositoryError(err)
		}
		created, err = s.hold(db, id, false)
		return err
	})
	return created, err
}

// ConfirmHold turns a live hold into the shipment's current CONFIRMED booking, cancelling the booking it replaces.
func (s *Store) ConfirmHold(ctx context.Context, id string) (*Hold, error) {
	return s.changeHold(ctx, id, func(db *gorm.DB, h *Hold, now time.Time) error {
		if !now.Before(h.ExpiresAt) {
			return errors.New(HoldExpiredCode, fmt.Sprintf("hold %s expired at %s", id, h.ExpiresAt.Format(time.RFC3339)))
		}
		if !h.IsCurrent && h.ReplacesID != nil {
			if err := db.Exec(`UPDATE appointments SET appointment_status = 'CANCELLED', is_current = 0,
					cancelled_at = setuhaul_now(), cancellation_reason = 'replaced by the driver''s choice ' || ?,
					updated_at = setuhaul_now()
				WHERE appointment_id = ? AND is_current = 1 AND appointment_status = 'CONFIRMED'`,
				id, *h.ReplacesID).Error; err != nil {
				return errors.RepositoryError(err)
			}
		}
		return errors.RepositoryError(db.Exec(`UPDATE appointments SET appointment_status = 'CONFIRMED', is_current = 1,
				confirmed_at = setuhaul_now(), updated_at = setuhaul_now()
			WHERE appointment_id = ?`, id).Error)
	})
}

// ReleaseHold cancels a hold at the driver's request.
func (s *Store) ReleaseHold(ctx context.Context, id string) (*Hold, error) {
	return s.changeHold(ctx, id, func(db *gorm.DB, h *Hold, _ time.Time) error {
		return errors.RepositoryError(db.Exec(`UPDATE appointments SET appointment_status = 'CANCELLED', is_current = 0,
				cancelled_at = setuhaul_now(), cancellation_reason = 'released by the driver', updated_at = setuhaul_now()
			WHERE appointment_id = ?`, id).Error)
	})
}

func (s *Store) changeHold(ctx context.Context, id string, change func(*gorm.DB, *Hold, time.Time) error) (*Hold, error) {
	var out *Hold
	err := s.DoInTransaction(ctx, func(ctx context.Context) error {
		db := s.GetDB(ctx)
		h, err := s.hold(db, id, false)
		if err != nil {
			return err
		}
		if err := lockFacility(db, h.FacilityID); err != nil {
			return errors.RepositoryError(err)
		}
		if h, err = s.hold(db, id, true); err != nil {
			return err
		}
		if h.Status != "PENDING_CONFIRMATION" {
			return errors.New(HoldConflictCode, fmt.Sprintf("hold %s is %s", id, h.Status))
		}
		var now time.Time
		if err := db.Raw("SELECT setuhaul_now()").Scan(&now).Error; err != nil {
			return errors.RepositoryError(err)
		}
		if err := change(db, h, now); err != nil {
			return err
		}
		out, err = s.hold(db, id, false)
		return err
	})
	return out, err
}

// ExpireHolds cancels every hold past its expiry, facility by facility under the booking lock. Returns how many.
func (s *Store) ExpireHolds(ctx context.Context) (int, error) {
	var facilities []string
	if err := s.GetDB(ctx).Raw(`SELECT DISTINCT sl.facility_id FROM appointments a
		JOIN appointment_slots sl ON sl.slot_id = a.slot_id
		WHERE a.appointment_status = 'PENDING_CONFIRMATION'
		  AND a.booked_at + make_interval(mins => ?) <= setuhaul_now()`, s.settings.HoldExpiryMin).
		Scan(&facilities).Error; err != nil {
		return 0, errors.RepositoryError(err)
	}
	total := 0
	for _, f := range facilities {
		err := s.DoInTransaction(ctx, func(ctx context.Context) error {
			db := s.GetDB(ctx)
			if err := lockFacility(db, f); err != nil {
				return errors.RepositoryError(err)
			}
			res := db.Exec(`UPDATE appointments a SET appointment_status = 'CANCELLED', is_current = 0,
					cancelled_at = setuhaul_now(), cancellation_reason = 'HOLD_EXPIRED', updated_at = setuhaul_now()
				FROM appointment_slots sl
				WHERE sl.slot_id = a.slot_id AND sl.facility_id = ? AND a.appointment_status = 'PENDING_CONFIRMATION'
				  AND a.booked_at + make_interval(mins => ?) <= setuhaul_now()`, f, s.settings.HoldExpiryMin)
			total += int(res.RowsAffected)
			return errors.RepositoryError(res.Error)
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
