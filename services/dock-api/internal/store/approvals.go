package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Approval request states.
const (
	RequestPending   = "PENDING"
	RequestApproved  = "APPROVED"
	RequestRejected  = "REJECTED"
	RequestExpired   = "EXPIRED"
	RequestWithdrawn = "WITHDRAWN"
)

// ApprovalRequest is a HIGH or CRITICAL truck's slot change waiting for (or decided by) a coordinator.
type ApprovalRequest struct {
	RequestID         string     `json:"requestId"`
	FacilityID        string     `json:"facilityId"`
	ShipmentID        string     `json:"shipmentId"`
	HoldAppointmentID string     `json:"holdAppointmentId"`
	Status            string     `json:"status"`
	PriorityCode      string     `json:"priorityCode"`
	RequestedAt       time.Time  `json:"requestedAt"`
	RequestedBy       string     `json:"requestedBy"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	DecidedAt         *time.Time `json:"decidedAt,omitempty"`
	DecidedBy         *string    `json:"decidedBy,omitempty"`
	Note              *string    `json:"note,omitempty"`
	ThreadID          *string    `json:"threadId,omitempty"`
	// What the coordinator decides between: the booking now (From) and the reserved slot (To).
	DriverName   string     `json:"driverName"`
	FromDockCode *string    `json:"fromDockCode,omitempty"`
	FromStart    *time.Time `json:"fromStart,omitempty"`
	ToSlotID     string     `json:"toSlotId"`
	ToDockCode   string     `json:"toDockCode"`
	ToStart      time.Time  `json:"toStart"`
}

const requestSQL = `
	SELECT r.request_id, r.facility_id, r.shipment_id, r.hold_appointment_id, r.request_status AS status,
	       r.priority_code, r.requested_at, r.requested_by, r.expires_at, r.decided_at, r.decided_by,
	       r.decision_note AS note, r.thread_id, d.driver_name,
	       fd.dock_code AS from_dock_code, fsl.slot_start_ts AS from_start,
	       hsl.slot_id AS to_slot_id, hd.dock_code AS to_dock_code, hsl.slot_start_ts AS to_start
	FROM slot_change_requests r
	JOIN shipments s ON s.shipment_id = r.shipment_id
	JOIN drivers d ON d.driver_id = s.driver_id
	JOIN appointments h ON h.appointment_id = r.hold_appointment_id
	JOIN appointment_slots hsl ON hsl.slot_id = h.slot_id
	JOIN docks hd ON hd.dock_id = hsl.dock_id
	LEFT JOIN LATERAL (
	    SELECT a.slot_id FROM appointments a
	    WHERE a.shipment_id = r.shipment_id AND a.appointment_id <> r.hold_appointment_id
	      AND ((r.request_status = 'PENDING' AND a.is_current = 1 AND a.appointment_status IN ('CONFIRMED', 'IN_PROGRESS'))
	           OR (r.request_status <> 'PENDING' AND a.appointment_id = r.from_appointment_id))
	    ORDER BY a.is_current DESC LIMIT 1) cur ON TRUE
	LEFT JOIN appointment_slots fsl ON fsl.slot_id = cur.slot_id
	LEFT JOIN docks fd ON fd.dock_id = fsl.dock_id`

// requestForHold returns the hold's pending request, else its latest one, else nil.
func requestForHold(db *gorm.DB, holdID string) (*ApprovalRequest, error) {
	var out []ApprovalRequest
	if err := db.Raw(requestSQL+` WHERE r.hold_appointment_id = ?`, holdID).Scan(&out).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// GetRequest returns one approval request.
func (s *Store) GetRequest(ctx context.Context, id string) (*ApprovalRequest, error) {
	return getRequest(s.GetDB(ctx), id)
}

func getRequest(db *gorm.DB, id string) (*ApprovalRequest, error) {
	var out []ApprovalRequest
	if err := db.Raw(requestSQL+` WHERE r.request_id = ?`, id).Scan(&out).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(out) == 0 {
		return nil, errors.NewRecordNotFoundError("no approval request " + id)
	}
	return &out[0], nil
}

// PendingRequests lists a facility's requests waiting for a coordinator, the soonest to expire first.
func (s *Store) PendingRequests(ctx context.Context, facilityID string) ([]ApprovalRequest, error) {
	out := []ApprovalRequest{}
	err := s.GetDB(ctx).Raw(requestSQL+` WHERE r.facility_id = ? AND r.request_status = 'PENDING'
		ORDER BY CASE r.priority_code WHEN 'CRITICAL' THEN 0 WHEN 'HIGH' THEN 1 ELSE 2 END, r.expires_at`,
		facilityID).Scan(&out).Error
	return out, errors.RepositoryError(err)
}

// requestApproval files a request for a hold beside a booking and keeps the hold's slot reserved until it expires.
func (s *Store) requestApproval(ctx context.Context, db *gorm.DB, h *Hold, current *AgentBooking, now time.Time) error {
	by, _ := utility.GetUserID(ctx)
	if by == "" {
		by = "unknown"
	}
	id := "REQ-" + strings.ToUpper(uuid.NewString()[:13])
	if err := db.Exec(`INSERT INTO slot_change_requests (request_id, facility_id, shipment_id, hold_appointment_id,
			from_appointment_id, thread_id, priority_code, request_status, requested_at, requested_by, expires_at)
		VALUES (?, ?, ?, ?, ?,
		        (SELECT t.thread_id FROM chat_threads t WHERE t.shipment_id = ? AND t.thread_status NOT IN ('RESOLVED', 'CLOSED')
		         ORDER BY t.opened_at DESC LIMIT 1),
		        ?, 'PENDING', ?, ?, ?)`,
		id, h.FacilityID, h.ShipmentID, h.AppointmentID, current.AppointmentID, h.ShipmentID, h.PriorityCode, now, by,
		now.Add(time.Duration(s.settings.ApprovalReserveMin)*time.Minute)).Error; err != nil {
		if errors.CodeOf(errors.RepositoryError(err)) == errors.DuplicateRequestErrorCode {
			return errors.Wrap(HoldConflictCode, h.ShipmentID+" already has a change waiting for approval", err)
		}
		return errors.RepositoryError(err)
	}
	return nil
}

// decideRequest closes a pending request and, for APPROVED, REJECTED and EXPIRED, tells the driver on the feed. The
// caller holds the facility's booking lock and changes the hold itself (after this call, so the feed row still sees
// the booking the request was decided against).
func decideRequest(db *gorm.DB, id, status, by, note string) error {
	var noteArg interface{}
	if note != "" {
		noteArg = note
	}
	res := db.Exec(`UPDATE slot_change_requests SET request_status = ?, decided_at = setuhaul_now(), decided_by = ?,
			decision_note = ?
		WHERE request_id = ? AND request_status = 'PENDING'`, status, by, noteArg, id)
	if res.Error != nil {
		return errors.RepositoryError(res.Error)
	}
	if res.RowsAffected == 0 {
		return errors.New(HoldConflictCode, "request "+id+" is no longer waiting for a decision")
	}
	if status == RequestWithdrawn {
		return nil
	}
	return errors.RepositoryError(db.Exec(`
		INSERT INTO booking_changes (facility_id, run_id, shipment_id, action, from_slot_id, to_slot_id, reason_code,
		    detail, changed_at, kind, request_id)
		SELECT r.facility_id, NULL, r.shipment_id, ?,
		       (SELECT a.slot_id FROM appointments a WHERE a.shipment_id = r.shipment_id AND a.is_current = 1
		          AND a.appointment_status IN ('CONFIRMED', 'IN_PROGRESS') AND a.appointment_id <> r.hold_appointment_id
		        LIMIT 1),
		       h.slot_id, NULL, r.decision_note, setuhaul_now(), 'APPROVAL', r.request_id
		FROM slot_change_requests r JOIN appointments h ON h.appointment_id = r.hold_appointment_id
		WHERE r.request_id = ?`, status, id).Error)
}

// DecideRequest approves or rejects a pending request as a coordinator. Approve swaps the reserved slot in for the
// truck's booking, exactly as a driver's confirmation does; reject releases the reservation and keeps the booking.
func (s *Store) DecideRequest(ctx context.Context, id string, approve bool, note string) (*ApprovalRequest, error) {
	if len(note) > 500 {
		return nil, errors.NewBadRequestError("note is limited to 500 characters")
	}
	by, _ := utility.GetUserID(ctx)
	if by == "" {
		by = "unknown"
	}
	req, err := s.GetRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	var out *ApprovalRequest
	err = s.DoInTransaction(ctx, func(ctx context.Context) error {
		db := s.GetDB(ctx)
		if err := lockFacility(db, req.FacilityID); err != nil {
			return errors.RepositoryError(err)
		}
		h, err := s.hold(db, req.HoldAppointmentID, true)
		if err != nil {
			return err
		}
		cur, err := getRequest(db, id)
		if err != nil {
			return err
		}
		if cur.Status != RequestPending {
			return errors.New(HoldConflictCode, fmt.Sprintf("request %s is %s", id, cur.Status))
		}
		if h.Status != "PENDING_CONFIRMATION" {
			return errors.New(HoldConflictCode, fmt.Sprintf("the reserved slot of request %s is %s", id, h.Status))
		}
		if !approve {
			if err := decideRequest(db, id, RequestRejected, by, note); err != nil {
				return err
			}
			if err := db.Exec(`UPDATE appointments SET appointment_status = 'REJECTED', is_current = 0,
					cancelled_at = setuhaul_now(), cancellation_reason = 'change rejected by a coordinator',
					updated_at = setuhaul_now()
				WHERE appointment_id = ?`, h.AppointmentID).Error; err != nil {
				return errors.RepositoryError(err)
			}
		} else {
			var now time.Time
			if err := db.Raw("SELECT setuhaul_now()").Scan(&now).Error; err != nil {
				return errors.RepositoryError(err)
			}
			if !now.Before(cur.ExpiresAt) {
				return errors.New(HoldExpiredCode, fmt.Sprintf("request %s expired at %s", id, cur.ExpiresAt.Format(time.RFC3339)))
			}
			if err := slotStillUsable(db, h); err != nil {
				return err
			}
			if err := decideRequest(db, id, RequestApproved, by, note); err != nil {
				return err
			}
			if err := swapIn(db, h, "replaced by the approved change "+id); err != nil {
				return err
			}
		}
		out, err = getRequest(db, id)
		return err
	})
	return out, err
}

// slotStillUsable checks what can change under a reservation: the slot blocked, the dock out of service, or a dock
// event over the truck's unload.
func slotStillUsable(db *gorm.DB, h *Hold) error {
	var why []string
	if err := db.Raw(`
		SELECT CASE WHEN sl.slot_status <> 'OPEN' THEN 'the slot is ' || sl.slot_status
		            WHEN d.dock_status <> 'ACTIVE' THEN 'dock ' || d.dock_code || ' is ' || d.dock_status
		            ELSE 'dock ' || d.dock_code || ' has a ' || ev.event_type END
		FROM appointment_slots sl JOIN docks d ON d.dock_id = sl.dock_id
		JOIN shipments s ON s.shipment_id = ?
		LEFT JOIN LATERAL (SELECT e.event_type FROM dock_status_events e
		    WHERE e.dock_id = d.dock_id AND e.created_at <= setuhaul_now()
		      AND e.event_start_ts < sl.slot_start_ts + make_interval(mins => GREATEST(s.expected_unload_min,
		          (EXTRACT(EPOCH FROM sl.slot_end_ts - sl.slot_start_ts) / 60)::int))
		      AND (e.event_end_ts IS NULL OR e.event_end_ts > sl.slot_start_ts) LIMIT 1) ev ON TRUE
		WHERE sl.slot_id = ? AND (sl.slot_status <> 'OPEN' OR d.dock_status <> 'ACTIVE' OR ev.event_type IS NOT NULL)`,
		h.ShipmentID, h.SlotID).Scan(&why).Error; err != nil {
		return errors.RepositoryError(err)
	}
	if len(why) > 0 {
		return errors.New(SlotUnavailableCode, fmt.Sprintf("slot %s can no longer be used (%s); reject the request",
			h.SlotID, why[0]))
	}
	return nil
}
