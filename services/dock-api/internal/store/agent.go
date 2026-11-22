package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/google/uuid"
)

// IST is the facility's zone; times in driver messages are shown in it.
var IST = time.FixedZone("IST", 5*3600+1800)

func hm(t time.Time) string { return t.In(IST).Format("15:04") }

// AgentBooking is a truck's current booking or live hold.
type AgentBooking struct {
	AppointmentID string     `json:"appointmentId"`
	SlotID        string     `json:"slotId"`
	DockCode      string     `json:"dockCode"`
	Start         time.Time  `json:"start"`
	End           time.Time  `json:"end"`
	Status        string     `json:"status"`
	Source        string     `json:"source"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
}

// AgentShipment is what the driver agent needs to answer "where is my slot?".
type AgentShipment struct {
	ShipmentID       string        `json:"shipmentId"`
	DriverName       string        `json:"driverName"`
	DriverPhone      string        `json:"driverPhone"`
	PriorityCode     string        `json:"priorityCode"`
	OriginCity       string        `json:"originCity"`
	Status           string        `json:"status"`
	Eta              *time.Time    `json:"eta,omitempty"`
	EtaSource        *string       `json:"etaSource,omitempty"`
	EtaConfidence    *string       `json:"etaConfidence,omitempty"`
	GateIn           *time.Time    `json:"gateIn,omitempty"`
	Booking          *AgentBooking `json:"booking,omitempty"`
	Hold             *AgentBooking `json:"hold,omitempty"`
	PlanStatus       *string       `json:"planStatus,omitempty"`
	PlanReason       *string       `json:"planReason,omitempty"`
	PredictedWaitMin *int          `json:"predictedWaitMin,omitempty"`
	Message          string        `json:"message"`
}

var nonDigits = regexp.MustCompile(`\D`)

// ShipmentsByPhone lists the open shipments of the driver with this phone number (matched on the last 10 digits).
func (s *Store) ShipmentsByPhone(ctx context.Context, phone string) ([]AgentShipment, error) {
	digits := nonDigits.ReplaceAllString(phone, "")
	if len(digits) < 10 {
		return nil, errors.NewBadRequestError("phone must have at least 10 digits")
	}
	var ids []string
	if err := s.GetDB(ctx).Raw(`
		SELECT s.shipment_id FROM shipments s JOIN drivers d ON d.driver_id = s.driver_id
		WHERE right(regexp_replace(d.phone, '\D', '', 'g'), 10) = right(?, 10)
		  AND s.current_status NOT IN ('COMPLETED', 'CANCELLED') AND s.created_at <= setuhaul_now()
		ORDER BY s.original_eta_ts, s.shipment_id`, digits).Scan(&ids).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(ids) == 0 {
		return nil, errors.NewRecordNotFoundError("no open shipment for that phone number")
	}
	out := make([]AgentShipment, 0, len(ids))
	for _, id := range ids {
		sh, err := s.AgentShipment(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *sh)
	}
	return out, nil
}

// AgentShipment returns one truck's booking, hold, ETA and plan status, with a message for the driver.
func (s *Store) AgentShipment(ctx context.Context, shipmentID string) (*AgentShipment, error) {
	db := s.GetDB(ctx)
	var rows []struct {
		ShipmentID    string
		DriverName    string
		DriverPhone   string
		PriorityCode  string
		OriginCity    string
		Status        string
		Eta           *time.Time
		EtaSource     *string
		EtaConfidence *string
		GateIn        *time.Time
	}
	if err := db.Raw(`
		SELECT s.shipment_id, d.driver_name, d.phone AS driver_phone, s.priority_code, s.origin_city,
		       s.current_status AS status, e.eta_ts AS eta, e.source AS eta_source, e.confidence AS eta_confidence,
		       fc.gate_in_ts AS gate_in
		FROM shipments s
		JOIN drivers d ON d.driver_id = s.driver_id
		LEFT JOIN LATERAL effective_eta(s.shipment_id, setuhaul_now()) e ON TRUE
		LEFT JOIN facility_checkins fc ON fc.shipment_id = s.shipment_id AND fc.gate_in_ts <= setuhaul_now()
		WHERE s.shipment_id = ? AND s.created_at <= setuhaul_now()`, shipmentID).Scan(&rows).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(rows) == 0 {
		return nil, errors.NewRecordNotFoundError("no shipment " + shipmentID)
	}
	r := rows[0]
	out := AgentShipment{ShipmentID: r.ShipmentID, DriverName: r.DriverName, DriverPhone: r.DriverPhone,
		PriorityCode: r.PriorityCode, OriginCity: r.OriginCity, Status: r.Status, Eta: r.Eta, EtaSource: r.EtaSource,
		EtaConfidence: r.EtaConfidence, GateIn: r.GateIn}
	var bookings []AgentBooking
	if err := db.Raw(`
		SELECT a.appointment_id, a.slot_id, dk.dock_code, sl.slot_start_ts AS start, sl.slot_end_ts AS "end",
		       a.appointment_status AS status, a.booking_source AS source,
		       CASE WHEN a.appointment_status = 'PENDING_CONFIRMATION'
		            THEN a.booked_at + make_interval(mins => ?) END AS expires_at
		FROM appointments a
		JOIN appointment_slots sl ON sl.slot_id = a.slot_id
		JOIN docks dk ON dk.dock_id = sl.dock_id
		WHERE a.shipment_id = ? AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS')
		ORDER BY a.is_current DESC, a.booked_at`, s.settings.HoldExpiryMin, shipmentID).Scan(&bookings).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	for i := range bookings {
		b := bookings[i]
		if b.Status == "PENDING_CONFIRMATION" {
			out.Hold = &b
		} else if out.Booking == nil {
			out.Booking = &b
		}
	}
	var plan []struct {
		AllocationStatus string
		ReasonCode       *string
		PredictedWaitMin *int
	}
	if err := db.Raw(`
		SELECT i.allocation_status, i.reason_code, i.predicted_wait_min FROM solver_plan_item i
		WHERE i.shipment_id = ? AND i.run_id = (
		    SELECT r.run_id FROM solver_run r JOIN shipments s ON s.destination_facility_id = r.facility_id
		    WHERE s.shipment_id = ? AND r.status = 'COMMITTED' ORDER BY r.committed_at DESC LIMIT 1)`,
		shipmentID, shipmentID).Scan(&plan).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(plan) > 0 {
		out.PlanStatus, out.PlanReason, out.PredictedWaitMin = &plan[0].AllocationStatus, plan[0].ReasonCode, plan[0].PredictedWaitMin
	}
	out.Message = driverMessage(&out)
	return &out, nil
}

// reasonWords turns the plan's reason codes into words for a driver.
var reasonWords = map[string]string{
	"SLOT_ALREADY_STARTED":     "your slot started before you could reach it",
	"TRUCK_CANNOT_MAKE_SLOT":   "your new ETA is later than the slot",
	"HOLD_EXPIRED":             "the hold expired",
	"BUMPED_FOR_CRITICAL":      "an urgent delivery needed that slot",
	"BLOCKED_BY_DOCK_EVENT":    "the dock is unavailable then",
	"BLOCKED_BY_BLOCKED_SLOT":  "the slot was blocked",
	"MOVED":                    "the plan changed",
	"NO_FREE_SLOT":             "every suitable slot is taken",
	"COMPATIBLE_SLOTS_BLOCKED": "every suitable dock is unavailable",
	"WEIGHT_BREACH":            "your load is heavier than the booked dock allows",
}

func words(code *string) string {
	if code == nil {
		return ""
	}
	if w, ok := reasonWords[*code]; ok {
		return w
	}
	return strings.ToLower(strings.ReplaceAll(*code, "_", " "))
}

func driverMessage(a *AgentShipment) string {
	switch {
	case a.GateIn != nil && a.Booking != nil:
		return fmt.Sprintf("You are checked in at the gate since %s. Your dock is %s at %s.", hm(*a.GateIn),
			a.Booking.DockCode, hm(a.Booking.Start))
	case a.GateIn != nil:
		return fmt.Sprintf("You are checked in at the gate since %s. A dock will be assigned shortly.", hm(*a.GateIn))
	case a.Hold != nil && a.Hold.ExpiresAt != nil:
		return fmt.Sprintf("You are holding dock %s at %s until %s. Confirm to keep it, or it is released.",
			a.Hold.DockCode, hm(a.Hold.Start), hm(*a.Hold.ExpiresAt))
	case a.PlanStatus != nil && *a.PlanStatus == "FLAGGED":
		return "Your booking is with a coordinator because " + words(a.PlanReason) + ". They will contact you."
	case a.Booking != nil:
		msg := fmt.Sprintf("Your slot is dock %s at %s.", a.Booking.DockCode, hm(a.Booking.Start))
		if a.Eta != nil {
			msg += fmt.Sprintf(" We expect you at %s.", hm(*a.Eta))
		}
		return msg
	case a.PlanStatus != nil && *a.PlanStatus == "UNASSIGNED":
		return "There is no safe slot for you yet because " + words(a.PlanReason) + ". A coordinator will contact you."
	default:
		return "Your request is being planned; you will get a slot shortly."
	}
}

// Change is one booking change for the agent, newest last.
type FeedChange struct {
	ChangeID     int64      `json:"changeId"`
	ShipmentID   string     `json:"shipmentId"`
	DriverPhone  string     `json:"driverPhone"`
	Action       string     `json:"action"`
	FromDockCode *string    `json:"fromDockCode,omitempty"`
	FromStart    *time.Time `json:"fromStart,omitempty"`
	ToDockCode   *string    `json:"toDockCode,omitempty"`
	ToStart      *time.Time `json:"toStart,omitempty"`
	ReasonCode   *string    `json:"reasonCode,omitempty"`
	ChangedAt    time.Time  `json:"changedAt"`
	Message      string     `json:"message"`
}

// ChangesAfter returns up to limit booking changes with change_id > after, oldest first, and the newest change id
// (0 when there is none), so an agent can tell that its cursor is past the end after the nightly reset.
func (s *Store) ChangesAfter(ctx context.Context, after int64, limit int) ([]FeedChange, int64, error) {
	out := []FeedChange{}
	var last int64
	if err := s.GetDB(ctx).Raw("SELECT COALESCE(max(change_id), 0) FROM booking_changes").Scan(&last).Error; err != nil {
		return nil, 0, errors.RepositoryError(err)
	}
	if err := s.GetDB(ctx).Raw(`
		SELECT c.change_id, c.shipment_id, d.phone AS driver_phone, c.action, fd.dock_code AS from_dock_code,
		       fs.slot_start_ts AS from_start, td.dock_code AS to_dock_code, ts.slot_start_ts AS to_start,
		       c.reason_code, c.changed_at
		FROM booking_changes c
		JOIN shipments s ON s.shipment_id = c.shipment_id
		JOIN drivers d ON d.driver_id = s.driver_id
		LEFT JOIN appointment_slots fs ON fs.slot_id = c.from_slot_id
		LEFT JOIN docks fd ON fd.dock_id = fs.dock_id
		LEFT JOIN appointment_slots ts ON ts.slot_id = c.to_slot_id
		LEFT JOIN docks td ON td.dock_id = ts.dock_id
		WHERE c.change_id > ?
		ORDER BY c.change_id
		LIMIT ?`, after, limit).Scan(&out).Error; err != nil {
		return nil, 0, errors.RepositoryError(err)
	}
	for i := range out {
		out[i].Message = changeMessage(&out[i])
	}
	return out, last, nil
}

func changeMessage(c *FeedChange) string {
	switch c.Action {
	case "NEW":
		return fmt.Sprintf("%s is booked on dock %s at %s.", c.ShipmentID, deref(c.ToDockCode), hmPtr(c.ToStart))
	case "MOVED":
		return fmt.Sprintf("%s moved from dock %s at %s to dock %s at %s because %s.", c.ShipmentID,
			deref(c.FromDockCode), hmPtr(c.FromStart), deref(c.ToDockCode), hmPtr(c.ToStart), words(c.ReasonCode))
	default:
		return fmt.Sprintf("%s lost its slot (dock %s at %s) because %s; a coordinator will follow up.", c.ShipmentID,
			deref(c.FromDockCode), hmPtr(c.FromStart), words(c.ReasonCode))
	}
}

func deref(s *string) string {
	if s == nil {
		return "?"
	}
	return *s
}

func hmPtr(t *time.Time) string {
	if t == nil {
		return "?"
	}
	return hm(*t)
}

// ETA sources a person or the agent may report.
const (
	SourceDriver     = "DRIVER_DECLARED"
	SourceOperations = "OPERATIONS_OVERRIDE"
)

var delayReasons = map[string]bool{"TRAFFIC": true, "BREAKDOWN": true, "WEATHER": true, "LATE_DEPARTURE": true,
	"LOADING_DELAY": true, "ROUTE_ISSUE": true, "OTHER": true}

// ReportETA records a new ETA for a truck (a driver's report or an operations override) as of the shared clock.
// It must lie between 1 hour ago and 48 hours ahead. Returns the shipment's facility.
func (s *Store) ReportETA(ctx context.Context, shipmentID string, eta time.Time, source, confidence, reason, note string) (string, error) {
	if source != SourceDriver && source != SourceOperations {
		return "", errors.NewBadRequestError("source must be DRIVER_DECLARED or OPERATIONS_OVERRIDE")
	}
	if confidence == "" {
		confidence = "MEDIUM"
	}
	if confidence != "LOW" && confidence != "MEDIUM" && confidence != "HIGH" {
		return "", errors.NewBadRequestError("confidence must be LOW, MEDIUM or HIGH")
	}
	if reason != "" && !delayReasons[reason] {
		return "", errors.NewBadRequestError("unknown reason " + reason)
	}
	if len(note) > 500 {
		return "", errors.NewBadRequestError("note is limited to 500 characters")
	}
	if err := s.shipmentOpen(ctx, shipmentID); err != nil {
		return "", err
	}
	now, err := s.Now(ctx)
	if err != nil {
		return "", err
	}
	if eta.Before(now.Add(-time.Hour)) || eta.After(now.Add(48*time.Hour)) {
		return "", errors.NewBadRequestError("eta must be between 1 hour ago and 48 hours ahead")
	}
	var facility string
	if err := s.GetDB(ctx).Raw("SELECT destination_facility_id FROM shipments WHERE shipment_id = ?", shipmentID).
		Scan(&facility).Error; err != nil {
		return "", errors.RepositoryError(err)
	}
	var reasonArg, noteArg interface{}
	if reason != "" {
		reasonArg = reason
	}
	if note != "" {
		noteArg = note
	}
	return facility, errors.RepositoryError(s.GetDB(ctx).Exec(`
		INSERT INTO eta_updates (eta_update_id, shipment_id, source_type, reported_by_driver_id, declared_eta_ts,
		    confidence_code, delay_reason_code, note, created_at)
		SELECT ?, s.shipment_id, ?, CASE WHEN ? = 'DRIVER_DECLARED' THEN s.driver_id END, ?, ?, ?, ?, setuhaul_now()
		FROM shipments s WHERE s.shipment_id = ?`,
		"ETA-A-"+strings.ToUpper(uuid.NewString()[:13]), source, source, eta, confidence, reasonArg, noteArg,
		shipmentID).Error)
}
