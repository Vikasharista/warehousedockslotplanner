package store

import (
	"context"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
)

// Dock is one dock of the facility.
type Dock struct {
	DockID       string `json:"dockId"`
	DockCode     string `json:"dockCode"`
	DockType     string `json:"dockType"`
	DockStatus   string `json:"dockStatus"`
	MaxWeightKg  int    `json:"maxWeightKg"`
	Refrigerated bool   `json:"refrigerated"`
}

// Booking is an active appointment on the board, with what the plan knows about the truck.
type Booking struct {
	AppointmentID    string     `json:"appointmentId"`
	ShipmentID       string     `json:"shipmentId"`
	SlotID           string     `json:"slotId"`
	DockID           string     `json:"dockId"`
	Status           string     `json:"status"`
	Source           string     `json:"source"`
	IsCurrent        bool       `json:"isCurrent"`
	BookedAt         time.Time  `json:"bookedAt"`
	HoldExpiresAt    *time.Time `json:"holdExpiresAt,omitempty"`
	SlotStart        time.Time  `json:"slotStart"`
	SlotEnd          time.Time  `json:"slotEnd"`
	PriorityCode     string     `json:"priorityCode"`
	VehicleID        string     `json:"vehicleId"`
	OriginCity       string     `json:"originCity"`
	LoadWeightKg     int        `json:"loadWeightKg"`
	UnloadMin        int        `json:"unloadMin"`
	EtaTs            *time.Time `json:"eta,omitempty"`
	EtaConfidence    *string    `json:"etaConfidence,omitempty"`
	EtaSource        *string    `json:"etaSource,omitempty"`
	GateInTs         *time.Time `json:"gateIn,omitempty"`
	PredictedWaitMin *int       `json:"predictedWaitMin,omitempty"`
	BookingAction    *string    `json:"bookingAction,omitempty"`
	Flagged          bool       `json:"flagged"`
}

// Block is a window in which a dock cannot take trucks: a dock event or a slot that is not OPEN.
type Block struct {
	DockID string     `json:"dockId"`
	Kind   string     `json:"kind"`
	Reason string     `json:"reason"`
	Start  time.Time  `json:"start"`
	End    *time.Time `json:"end,omitempty"`
}

// RunSummary is one solver run without its items.
type RunSummary struct {
	RunID          string     `json:"runId"`
	AsOf           time.Time  `json:"asOf"`
	Trigger        string     `json:"trigger"`
	TriggerDetail  *string    `json:"triggerDetail,omitempty"`
	Status         string     `json:"status"`
	Outcome        *string    `json:"outcome,omitempty"`
	Assigned       int        `json:"assigned"`
	Unassigned     int        `json:"unassigned"`
	AvgWaitMin     float64    `json:"avgWaitMin"`
	WeightedWait   float64    `json:"weightedWaitMin"`
	Inversions     int        `json:"priorityInversions"`
	UtilisationPct float64    `json:"dockUtilisationPct"`
	SolveMs        int        `json:"solveMs"`
	CommittedAt    *time.Time `json:"committedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// Board is the dock sequence screen: every dock with its bookings and blocks over the planning window.
type Board struct {
	FacilityID    string      `json:"facilityId"`
	Now           time.Time   `json:"now"`
	WindowStart   time.Time   `json:"windowStart"`
	WindowEnd     time.Time   `json:"windowEnd"`
	Docks         []Dock      `json:"docks"`
	Bookings      []Booking   `json:"bookings"`
	Blocks        []Block     `json:"blocks"`
	LastCommitted *RunSummary `json:"lastCommitted,omitempty"`
	LastRun       *RunSummary `json:"lastRun,omitempty"`
}

const runColumns = `run_id, as_of_ts AS as_of, run_trigger AS trigger, trigger_detail, status, outcome, assigned, unassigned,
	avg_wait_min, weighted_wait_min AS weighted_wait, priority_inversions AS inversions,
	dock_utilisation_pct AS utilisation_pct, solve_ms, committed_at, created_at`

// Board returns the facility's docks, bookings and blocks from two hours ago to the end of the horizon.
func (s *Store) Board(ctx context.Context, facilityID string) (*Board, error) {
	db := s.GetDB(ctx)
	b := &Board{FacilityID: facilityID}
	if err := db.Raw("SELECT setuhaul_now()").Scan(&b.Now).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	b.WindowStart = b.Now.Add(-2 * time.Hour)
	b.WindowEnd = b.Now.Add(time.Duration(s.settings.HorizonHours) * time.Hour)

	if err := db.Raw(`SELECT dock_id, dock_code, dock_type, dock_status, max_vehicle_weight_kg AS max_weight_kg,
			supports_refrigerated = 1 AS refrigerated
		FROM docks WHERE facility_id = ? ORDER BY dock_code`, facilityID).Scan(&b.Docks).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	var err error
	if b.LastCommitted, err = s.lastRun(ctx, facilityID, true); err != nil {
		return nil, err
	}
	if b.LastRun, err = s.lastRun(ctx, facilityID, false); err != nil {
		return nil, err
	}
	committed := ""
	if b.LastCommitted != nil {
		committed = b.LastCommitted.RunID
	}
	if err := db.Raw(`
		SELECT a.appointment_id, a.shipment_id, a.slot_id, sl.dock_id, a.appointment_status AS status,
		       a.booking_source AS source, a.is_current = 1 AS is_current, a.booked_at,
		       CASE WHEN a.appointment_status = 'PENDING_CONFIRMATION'
		            THEN a.booked_at + make_interval(mins => ?) END AS hold_expires_at,
		       sl.slot_start_ts AS slot_start, sl.slot_end_ts AS slot_end, s.priority_code, s.vehicle_id,
		       s.origin_city, s.load_weight_kg, s.expected_unload_min AS unload_min,
		       e.eta_ts, e.confidence AS eta_confidence, e.source AS eta_source, fc.gate_in_ts,
		       pi.predicted_wait_min, pi.booking_action,
		       COALESCE(pf.allocation_status = 'FLAGGED', FALSE) AS flagged
		FROM appointments a
		JOIN appointment_slots sl ON sl.slot_id = a.slot_id
		JOIN shipments s ON s.shipment_id = a.shipment_id
		LEFT JOIN LATERAL effective_eta(a.shipment_id, ?) e ON TRUE
		LEFT JOIN facility_checkins fc ON fc.shipment_id = a.shipment_id AND fc.gate_in_ts <= ?
		LEFT JOIN solver_plan_item pi ON pi.run_id = ? AND pi.shipment_id = a.shipment_id AND pi.slot_id = a.slot_id
		LEFT JOIN solver_plan_item pf ON pf.run_id = ? AND pf.shipment_id = a.shipment_id
		WHERE sl.facility_id = ? AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS')
		  AND sl.slot_end_ts >= ? AND sl.slot_start_ts < ?
		ORDER BY sl.dock_id, sl.slot_start_ts, a.shipment_id`,
		s.settings.HoldExpiryMin, b.Now, b.Now, committed, committed, facilityID, b.WindowStart, b.WindowEnd,
	).Scan(&b.Bookings).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if err := db.Raw(`
		SELECT ev.dock_id, ev.event_type AS kind, ev.reason, ev.event_start_ts AS start, ev.event_end_ts AS "end"
		FROM dock_status_events ev JOIN docks d ON d.dock_id = ev.dock_id
		WHERE d.facility_id = ? AND ev.created_at <= ? AND ev.event_start_ts < ?
		  AND (ev.event_end_ts IS NULL OR ev.event_end_ts > ?)
		UNION ALL
		SELECT sl.dock_id, 'SLOT_' || sl.slot_status, COALESCE(sl.block_reason, ''), sl.slot_start_ts, sl.slot_end_ts
		FROM appointment_slots sl
		WHERE sl.facility_id = ? AND sl.slot_status <> 'OPEN' AND sl.slot_start_ts < ? AND sl.slot_end_ts > ?
		ORDER BY 1, 4`,
		facilityID, b.Now, b.WindowEnd, b.WindowStart, facilityID, b.WindowEnd, b.WindowStart,
	).Scan(&b.Blocks).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	return b, nil
}

func (s *Store) lastRun(ctx context.Context, facilityID string, committedOnly bool) (*RunSummary, error) {
	q := "SELECT " + runColumns + " FROM solver_run WHERE facility_id = ?"
	if committedOnly {
		q += " AND status = 'COMMITTED' ORDER BY committed_at DESC LIMIT 1"
	} else {
		q += " ORDER BY created_at DESC LIMIT 1"
	}
	var runs []RunSummary
	if err := s.GetDB(ctx).Raw(q, facilityID).Scan(&runs).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return &runs[0], nil
}

// Runs returns the facility's newest runs.
func (s *Store) Runs(ctx context.Context, facilityID string, limit int) ([]RunSummary, error) {
	runs := []RunSummary{}
	err := s.GetDB(ctx).Raw("SELECT "+runColumns+" FROM solver_run WHERE facility_id = ? ORDER BY created_at DESC LIMIT ?",
		facilityID, limit).Scan(&runs).Error
	return runs, errors.RepositoryError(err)
}

// Exception is a truck the last committed plan could not, or must not, place: for a coordinator.
type Exception struct {
	ShipmentID       string     `json:"shipmentId"`
	AllocationStatus string     `json:"allocationStatus"`
	ReasonCode       *string    `json:"reasonCode,omitempty"`
	Detail           *string    `json:"detail,omitempty"`
	PriorityCode     string     `json:"priorityCode"`
	VehicleID        string     `json:"vehicleId"`
	OriginCity       string     `json:"originCity"`
	LoadWeightKg     int        `json:"loadWeightKg"`
	EarliestReady    *time.Time `json:"earliestReady,omitempty"`
	DockID           *string    `json:"dockId,omitempty"`
	OriginalSlotID   *string    `json:"originalSlotId,omitempty"`
}

// Exceptions lists the last committed run's unassigned, flagged, in-dock and beyond-horizon trucks.
func (s *Store) Exceptions(ctx context.Context, facilityID string) ([]Exception, error) {
	out := []Exception{}
	err := s.GetDB(ctx).Raw(`
		SELECT i.shipment_id, i.allocation_status, i.reason_code, i.detail, s.priority_code, s.vehicle_id,
		       s.origin_city, s.load_weight_kg, i.earliest_ready_ts AS earliest_ready, i.dock_id, i.original_slot_id
		FROM solver_plan_item i JOIN shipments s ON s.shipment_id = i.shipment_id
		WHERE i.run_id = (SELECT run_id FROM solver_run WHERE facility_id = ? AND status = 'COMMITTED'
		                  ORDER BY committed_at DESC LIMIT 1)
		  AND i.allocation_status <> 'ALLOCATED'
		ORDER BY CASE i.allocation_status WHEN 'UNASSIGNED' THEN 0 WHEN 'FLAGGED' THEN 1 WHEN 'IN_DOCK' THEN 2 ELSE 3 END,
		         i.shipment_id`, facilityID).Scan(&out).Error
	return out, errors.RepositoryError(err)
}

// Change is one booking change a committed run made.
type Change struct {
	RunID         string     `json:"runId"`
	CommittedAt   time.Time  `json:"committedAt"`
	Trigger       string     `json:"trigger"`
	TriggerDetail *string    `json:"triggerDetail,omitempty"`
	ShipmentID    string     `json:"shipmentId"`
	Action        string     `json:"action"`
	FromSlotID    *string    `json:"fromSlotId,omitempty"`
	FromDockCode  *string    `json:"fromDockCode,omitempty"`
	FromStart     *time.Time `json:"fromStart,omitempty"`
	ToSlotID      *string    `json:"toSlotId,omitempty"`
	ToDockCode    *string    `json:"toDockCode,omitempty"`
	ToStart       *time.Time `json:"toStart,omitempty"`
	ReasonCode    *string    `json:"reasonCode,omitempty"`
}

// Changes is the change log, newest first: what the agent tells drivers.
func (s *Store) Changes(ctx context.Context, facilityID string, limit int) ([]Change, error) {
	out := []Change{}
	err := s.GetDB(ctx).Raw(`
		SELECT c.run_id, c.committed_at, c.run_trigger AS trigger, c.trigger_detail, c.shipment_id,
		       c.booking_action AS action, c.from_slot_id, fd.dock_code AS from_dock_code, fs.slot_start_ts AS from_start,
		       c.to_slot_id, td.dock_code AS to_dock_code, c.to_start_ts AS to_start, c.reason_code
		FROM v_change_log c
		LEFT JOIN appointment_slots fs ON fs.slot_id = c.from_slot_id
		LEFT JOIN docks fd ON fd.dock_id = fs.dock_id
		LEFT JOIN docks td ON td.dock_id = c.to_dock_id
		WHERE c.facility_id = ?
		ORDER BY c.committed_at DESC, c.shipment_id
		LIMIT ?`, facilityID, limit).Scan(&out).Error
	return out, errors.RepositoryError(err)
}
