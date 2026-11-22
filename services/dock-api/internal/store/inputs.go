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

// ShipmentRow is one truck on the shipments screen.
type ShipmentRow struct {
	ShipmentID       string     `json:"shipmentId"`
	PriorityCode     string     `json:"priorityCode"`
	OriginCity       string     `json:"originCity"`
	RequiredDockType string     `json:"requiredDockType"`
	LoadWeightKg     int        `json:"loadWeightKg"`
	UnloadMin        int        `json:"unloadMin"`
	Status           string     `json:"status"`
	DriverName       string     `json:"driverName"`
	DriverPhone      string     `json:"driverPhone"`
	CreatedAt        time.Time  `json:"createdAt"`
	Eta              *time.Time `json:"eta,omitempty"`
	EtaSource        *string    `json:"etaSource,omitempty"`
	GateIn           *time.Time `json:"gateIn,omitempty"`
	DockCode         *string    `json:"dockCode,omitempty"`
	SlotStart        *time.Time `json:"slotStart,omitempty"`
	BookingStatus    *string    `json:"bookingStatus,omitempty"`
	PlanStatus       *string    `json:"planStatus,omitempty"`
	PlanReason       *string    `json:"planReason,omitempty"`
}

// Shipments lists the facility's open truck requests made so far.
func (s *Store) Shipments(ctx context.Context, facilityID string) ([]ShipmentRow, error) {
	out := []ShipmentRow{}
	err := s.GetDB(ctx).Raw(`
		WITH now AS (SELECT setuhaul_now() AS ts),
		run AS (SELECT run_id FROM solver_run WHERE facility_id = ? AND status = 'COMMITTED'
		        ORDER BY committed_at DESC LIMIT 1)
		SELECT s.shipment_id, s.priority_code, s.origin_city, s.required_dock_type, s.load_weight_kg,
		       s.expected_unload_min AS unload_min, s.current_status AS status, d.driver_name, d.phone AS driver_phone,
		       s.created_at, e.eta_ts AS eta, e.source AS eta_source, fc.gate_in_ts AS gate_in,
		       bk.dock_code, bk.slot_start_ts AS slot_start, bk.appointment_status AS booking_status,
		       pi.allocation_status AS plan_status, pi.reason_code AS plan_reason
		FROM shipments s
		CROSS JOIN now
		JOIN drivers d ON d.driver_id = s.driver_id
		LEFT JOIN LATERAL effective_eta(s.shipment_id, now.ts) e ON TRUE
		LEFT JOIN facility_checkins fc ON fc.shipment_id = s.shipment_id AND fc.gate_in_ts <= now.ts
		LEFT JOIN LATERAL (
		    SELECT dk.dock_code, sl.slot_start_ts, a.appointment_status FROM appointments a
		    JOIN appointment_slots sl ON sl.slot_id = a.slot_id JOIN docks dk ON dk.dock_id = sl.dock_id
		    WHERE a.shipment_id = s.shipment_id AND a.is_current = 1
		      AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS')
		    LIMIT 1) bk ON TRUE
		LEFT JOIN solver_plan_item pi ON pi.shipment_id = s.shipment_id AND pi.run_id = (SELECT run_id FROM run)
		WHERE s.destination_facility_id = ? AND s.created_at <= now.ts
		  AND s.current_status NOT IN ('COMPLETED', 'CANCELLED')
		ORDER BY COALESCE(e.eta_ts, s.original_eta_ts), s.shipment_id`, facilityID, facilityID).Scan(&out).Error
	return out, errors.RepositoryError(err)
}

// NewShipment is a truck request entered by hand.
type NewShipment struct {
	PriorityCode       string    `json:"priorityCode"`
	RequiredDockType   string    `json:"requiredDockType"`
	LoadWeightKg       int       `json:"loadWeightKg"`
	UnloadMin          int       `json:"unloadMin"`
	OriginCity         string    `json:"originCity"`
	Eta                time.Time `json:"eta"`
	CustomerName       string    `json:"customerName"`
	ProductCategory    string    `json:"productCategory"`
	TemperatureControl bool      `json:"temperatureControl"`
}

var (
	priorities = map[string]bool{"LOW": true, "NORMAL": true, "HIGH": true, "CRITICAL": true}
	dockTypes  = map[string]bool{"ANY": true, "STANDARD": true, "REEFER": true, "HEAVY": true}
)

func validateShape(priority, dockType string, weight, unload int) error {
	if !priorities[priority] {
		return errors.NewBadRequestError("priorityCode must be LOW, NORMAL, HIGH or CRITICAL")
	}
	if !dockTypes[dockType] {
		return errors.NewBadRequestError("requiredDockType must be ANY, STANDARD, REEFER or HEAVY")
	}
	if weight < 500 || weight > 32000 {
		return errors.NewBadRequestError("loadWeightKg must be between 500 and 32000")
	}
	if unload < 15 || unload > 240 {
		return errors.NewBadRequestError("unloadMin must be between 15 and 240")
	}
	return nil
}

// vehicleFor picks a vehicle type that can carry the load.
func vehicleFor(dockType string, weight int, reefer bool) (string, int, error) {
	switch {
	case reefer || dockType == "REEFER":
		if weight > 18000 {
			return "", 0, errors.NewBadRequestError("reefer loads are limited to 18000 kg")
		}
		return "REEFER_32", 18000, nil
	case dockType == "HEAVY" || weight > 22000:
		return "HEAVY_40", 32000, nil
	case weight <= 9000:
		return "20FT", 9000, nil
	case weight <= 15000:
		return "32FT_SXL", 15000, nil
	default:
		return "32FT_MXL", 22000, nil
	}
}

// CreateShipment adds a truck request made now, with its own driver and vehicle record (SHP-M0001, ...).
func (s *Store) CreateShipment(ctx context.Context, facilityID string, in NewShipment) (*ShipmentRow, error) {
	in.PriorityCode = strings.ToUpper(strings.TrimSpace(in.PriorityCode))
	in.RequiredDockType = strings.ToUpper(strings.TrimSpace(in.RequiredDockType))
	if in.RequiredDockType == "" {
		in.RequiredDockType = "ANY"
	}
	if err := validateShape(in.PriorityCode, in.RequiredDockType, in.LoadWeightKg, in.UnloadMin); err != nil {
		return nil, err
	}
	in.OriginCity = strings.TrimSpace(in.OriginCity)
	if in.OriginCity == "" || len(in.OriginCity) > 60 {
		return nil, errors.NewBadRequestError("originCity is required (up to 60 characters)")
	}
	if in.CustomerName == "" {
		in.CustomerName = "Walk-in request"
	}
	if in.ProductCategory == "" {
		in.ProductCategory = "General cargo"
	}
	if len(in.CustomerName) > 80 || len(in.ProductCategory) > 80 {
		return nil, errors.NewBadRequestError("customerName and productCategory are limited to 80 characters")
	}
	reefer := in.TemperatureControl || in.RequiredDockType == "REEFER"
	vtype, capacity, err := vehicleFor(in.RequiredDockType, in.LoadWeightKg, reefer)
	if err != nil {
		return nil, err
	}
	now, err := s.Now(ctx)
	if err != nil {
		return nil, err
	}
	if !in.Eta.After(now) || in.Eta.After(now.Add(24*time.Hour)) {
		return nil, errors.NewBadRequestError("eta must be in the next 24 hours")
	}
	var id string
	err = s.DoInTransaction(ctx, func(ctx context.Context) error {
		db := s.GetDB(ctx)
		var n int64
		if err := db.Raw("SELECT nextval('manual_request_seq')").Scan(&n).Error; err != nil {
			return errors.RepositoryError(err)
		}
		num := fmt.Sprintf("%04d", n)
		id = "SHP-M" + num
		var carrier string
		if err := db.Raw("SELECT carrier_id FROM carriers ORDER BY carrier_id LIMIT 1").Scan(&carrier).Error; err != nil {
			return errors.RepositoryError(err)
		}
		steps := []struct {
			sql  string
			args []interface{}
		}{
			{`INSERT INTO drivers (driver_id, carrier_id, driver_name, phone, licence_number, home_base_city, driver_status)
			  VALUES (?, ?, ?, ?, ?, ?, 'ACTIVE')`,
				[]interface{}{"DRV-M" + num, carrier, "Driver " + num, "+91-6400" + num + "00", "RJ-MAN-" + num, in.OriginCity}},
			{`INSERT INTO vehicles (vehicle_id, carrier_id, vehicle_type_code, registration_number, capacity_kg,
			      refrigeration_capable, active_flag) VALUES (?, ?, ?, ?, ?, ?, 1)`,
				[]interface{}{"VEH-M" + num, carrier, vtype, "RJ14 MX " + num, capacity, boolInt(reefer)}},
			{`INSERT INTO shipments (shipment_id, order_reference, carrier_id, driver_id, vehicle_id, origin_name, origin_city,
			      destination_facility_id, customer_name, product_category, load_weight_kg, pallet_count, required_dock_type,
			      temperature_control_required, priority_code, planned_departure_ts, original_eta_ts, expected_unload_min,
			      current_status, created_at, updated_at)
			  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ASSIGNED', setuhaul_now(), setuhaul_now())`,
				[]interface{}{id, "ORD-M" + num, carrier, "DRV-M" + num, "VEH-M" + num, in.OriginCity + " depot", in.OriginCity,
					facilityID, in.CustomerName, in.ProductCategory, in.LoadWeightKg, in.LoadWeightKg / 900,
					in.RequiredDockType, boolInt(reefer), in.PriorityCode, in.Eta.Add(-3 * time.Hour), in.Eta, in.UnloadMin}},
		}
		for _, st := range steps {
			if err := db.Exec(st.sql, st.args...).Error; err != nil {
				return errors.RepositoryError(err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.shipmentRow(ctx, facilityID, id)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) shipmentRow(ctx context.Context, facilityID, id string) (*ShipmentRow, error) {
	rows, err := s.Shipments(ctx, facilityID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ShipmentID == id {
			return &rows[i], nil
		}
	}
	return nil, errors.NewRecordNotFoundError("no open shipment " + id)
}

// ShipmentEdit changes the fields that drive planning; empty fields stay as they are.
type ShipmentEdit struct {
	PriorityCode     string `json:"priorityCode"`
	RequiredDockType string `json:"requiredDockType"`
	LoadWeightKg     int    `json:"loadWeightKg"`
	UnloadMin        int    `json:"unloadMin"`
}

// UpdateShipment edits a truck request; returns its facility.
func (s *Store) UpdateShipment(ctx context.Context, shipmentID string, in ShipmentEdit) (string, error) {
	var cur []struct {
		PriorityCode, RequiredDockType, DestinationFacilityID string
		LoadWeightKg, ExpectedUnloadMin                       int
	}
	if err := s.GetDB(ctx).Raw(`SELECT priority_code, required_dock_type, destination_facility_id, load_weight_kg,
		expected_unload_min FROM shipments WHERE shipment_id = ? AND current_status NOT IN ('COMPLETED', 'CANCELLED')
		AND created_at <= setuhaul_now()`, shipmentID).Scan(&cur).Error; err != nil {
		return "", errors.RepositoryError(err)
	}
	if len(cur) == 0 {
		return "", errors.NewRecordNotFoundError("no open shipment " + shipmentID)
	}
	c := cur[0]
	pick := func(v, def string) string {
		if v = strings.ToUpper(strings.TrimSpace(v)); v != "" {
			return v
		}
		return def
	}
	pickInt := func(v, def int) int {
		if v != 0 {
			return v
		}
		return def
	}
	p, d := pick(in.PriorityCode, c.PriorityCode), pick(in.RequiredDockType, c.RequiredDockType)
	w, u := pickInt(in.LoadWeightKg, c.LoadWeightKg), pickInt(in.UnloadMin, c.ExpectedUnloadMin)
	if err := validateShape(p, d, w, u); err != nil {
		return "", err
	}
	return c.DestinationFacilityID, errors.RepositoryError(s.GetDB(ctx).Exec(`UPDATE shipments SET priority_code = ?,
		required_dock_type = ?, load_weight_kg = ?, expected_unload_min = ?, temperature_control_required =
		CASE WHEN ? = 'REEFER' THEN 1 ELSE temperature_control_required END, updated_at = setuhaul_now()
		WHERE shipment_id = ?`, p, d, w, u, d, shipmentID).Error)
}

// CheckIn records a truck at the gate now. 409 if it already checked in.
func (s *Store) CheckIn(ctx context.Context, shipmentID string) error {
	if err := s.shipmentOpen(ctx, shipmentID); err != nil {
		return err
	}
	return s.DoInTransaction(ctx, func(ctx context.Context) error {
		db := s.GetDB(ctx)
		var existing []struct{ GateInTs *time.Time }
		if err := db.Raw("SELECT gate_in_ts FROM facility_checkins WHERE shipment_id = ? FOR UPDATE", shipmentID).
			Scan(&existing).Error; err != nil {
			return errors.RepositoryError(err)
		}
		if len(existing) > 0 && existing[0].GateInTs != nil {
			return errors.New(HoldConflictCode, shipmentID+" is already checked in")
		}
		// Arrival state against the effective ETA: more than 30 minutes either side is early or late.
		state := `CASE WHEN setuhaul_now() < e.eta_ts - interval '30 minutes' THEN 'EARLY'
		               WHEN setuhaul_now() > e.eta_ts + interval '30 minutes' THEN 'LATE' ELSE 'ON_TIME' END`
		if len(existing) > 0 {
			return errors.RepositoryError(db.Exec(`UPDATE facility_checkins fc SET gate_in_ts = setuhaul_now(),
				arrival_state = `+state+`, queue_state = 'NOT_QUEUED', updated_at = setuhaul_now()
				FROM effective_eta(?, setuhaul_now()) e WHERE fc.shipment_id = ?`, shipmentID, shipmentID).Error)
		}
		return errors.RepositoryError(db.Exec(`INSERT INTO facility_checkins (checkin_id, shipment_id, facility_id,
				gate_in_ts, arrival_state, queue_state, updated_at)
			SELECT ?, s.shipment_id, s.destination_facility_id, setuhaul_now(), `+state+`, 'NOT_QUEUED', setuhaul_now()
			FROM shipments s CROSS JOIN LATERAL effective_eta(s.shipment_id, setuhaul_now()) e
			WHERE s.shipment_id = ?`, "CHK-M-"+strings.ToUpper(uuid.NewString()[:13]), shipmentID).Error)
	})
}

// CapacitySlot is one slot on the capacity screen.
type CapacitySlot struct {
	SlotID     string    `json:"slotId"`
	DockID     string    `json:"dockId"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Status     string    `json:"status"`
	Reason     *string   `json:"reason,omitempty"`
	ShipmentID *string   `json:"shipmentId,omitempty"`
}

// Capacity is the capacity screen: docks, their events and the day's slots.
type Capacity struct {
	Docks  []Dock         `json:"docks"`
	Events []Block        `json:"events"`
	Slots  []CapacitySlot `json:"slots"`
}

// CapacityView returns the facility's docks, dock events from two hours ago on, and the slots from then on.
func (s *Store) CapacityView(ctx context.Context, facilityID string) (*Capacity, error) {
	db := s.GetDB(ctx)
	c := &Capacity{Docks: []Dock{}, Events: []Block{}, Slots: []CapacitySlot{}}
	if err := db.Raw(`SELECT dock_id, dock_code, dock_type, dock_status, max_vehicle_weight_kg AS max_weight_kg,
		supports_refrigerated = 1 AS refrigerated FROM docks WHERE facility_id = ? ORDER BY dock_code`, facilityID).
		Scan(&c.Docks).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if err := db.Raw(`SELECT ev.dock_id, ev.event_type AS kind, ev.reason, ev.event_start_ts AS start, ev.event_end_ts AS "end"
		FROM dock_status_events ev JOIN docks d ON d.dock_id = ev.dock_id
		WHERE d.facility_id = ? AND ev.created_at <= setuhaul_now()
		  AND (ev.event_end_ts IS NULL OR ev.event_end_ts > setuhaul_now() - interval '2 hours')
		ORDER BY ev.event_start_ts`, facilityID).Scan(&c.Events).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if err := db.Raw(`SELECT sl.slot_id, sl.dock_id, sl.slot_start_ts AS start, sl.slot_end_ts AS "end",
		sl.slot_status AS status, sl.block_reason AS reason,
		(SELECT a.shipment_id FROM appointments a WHERE a.slot_id = sl.slot_id
		   AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS') LIMIT 1) AS shipment_id
		FROM appointment_slots sl
		WHERE sl.facility_id = ? AND sl.slot_end_ts > setuhaul_now() - interval '2 hours'
		ORDER BY sl.dock_id, sl.slot_start_ts`, facilityID).Scan(&c.Slots).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	return c, nil
}

// NewDockEvent is a breakdown, maintenance, capacity reduction or manual block on one dock.
type NewDockEvent struct {
	DockID string     `json:"dockId"`
	Type   string     `json:"type"`
	Start  time.Time  `json:"start"`
	End    *time.Time `json:"end"`
	Reason string     `json:"reason"`
}

var eventTypes = map[string]bool{"MAINTENANCE": true, "BREAKDOWN": true, "CAPACITY_REDUCTION": true, "MANUAL_BLOCK": true}

// AddDockEvent records a dock event, known from now. Returns the dock's code for the re-plan note.
func (s *Store) AddDockEvent(ctx context.Context, facilityID string, in NewDockEvent) (string, error) {
	in.Type = strings.ToUpper(strings.TrimSpace(in.Type))
	if !eventTypes[in.Type] {
		return "", errors.NewBadRequestError("type must be MAINTENANCE, BREAKDOWN, CAPACITY_REDUCTION or MANUAL_BLOCK")
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || len(in.Reason) > 200 {
		return "", errors.NewBadRequestError("reason is required (up to 200 characters)")
	}
	if in.Start.IsZero() || (in.End != nil && !in.End.After(in.Start)) {
		return "", errors.NewBadRequestError("start is required and end must be after it")
	}
	code, err := s.dockCode(s.GetDB(ctx), facilityID, in.DockID)
	if err != nil {
		return "", err
	}
	return code, errors.RepositoryError(s.GetDB(ctx).Exec(`INSERT INTO dock_status_events (dock_event_id, dock_id, event_type,
		event_start_ts, event_end_ts, reason, created_at) VALUES (?, ?, ?, ?, ?, ?, setuhaul_now())`,
		"EVT-M-"+strings.ToUpper(uuid.NewString()[:13]), in.DockID, in.Type, in.Start, in.End, in.Reason).Error)
}

func (s *Store) dockCode(db *gorm.DB, facilityID, dockID string) (string, error) {
	var codes []string
	if err := db.Raw("SELECT dock_code FROM docks WHERE dock_id = ? AND facility_id = ?", dockID, facilityID).
		Scan(&codes).Error; err != nil {
		return "", errors.RepositoryError(err)
	}
	if len(codes) == 0 {
		return "", errors.NewRecordNotFoundError("no dock " + dockID + " at " + facilityID)
	}
	return codes[0], nil
}

// SetSlotStatus blocks (with a reason) or reopens a slot. Returns the slot's facility and a label for notes.
func (s *Store) SetSlotStatus(ctx context.Context, slotID, status, reason string) (string, string, error) {
	status = strings.ToUpper(status)
	if status != "OPEN" && status != "BLOCKED" {
		return "", "", errors.NewBadRequestError("status must be OPEN or BLOCKED")
	}
	reason = strings.TrimSpace(reason)
	if status == "BLOCKED" && (reason == "" || len(reason) > 200) {
		return "", "", errors.NewBadRequestError("a blocked slot needs a reason (up to 200 characters)")
	}
	var rows []struct {
		FacilityID  string
		DockCode    string
		SlotStartTs time.Time
	}
	if err := s.GetDB(ctx).Raw(`SELECT sl.facility_id, d.dock_code, sl.slot_start_ts FROM appointment_slots sl
		JOIN docks d ON d.dock_id = sl.dock_id WHERE sl.slot_id = ?`, slotID).Scan(&rows).Error; err != nil {
		return "", "", errors.RepositoryError(err)
	}
	if len(rows) == 0 {
		return "", "", errors.NewRecordNotFoundError("no slot " + slotID)
	}
	var reasonArg interface{}
	if status == "BLOCKED" {
		reasonArg = reason
	}
	err := s.GetDB(ctx).Exec("UPDATE appointment_slots SET slot_status = ?, block_reason = ? WHERE slot_id = ?",
		status, reasonArg, slotID).Error
	return rows[0].FacilityID, rows[0].DockCode + " " + hm(rows[0].SlotStartTs), errors.RepositoryError(err)
}

// SetDockStatus takes a dock in or out of service. Returns the dock's code.
func (s *Store) SetDockStatus(ctx context.Context, facilityID, dockID, status string) (string, error) {
	status = strings.ToUpper(status)
	if status != "ACTIVE" && status != "MAINTENANCE" && status != "OUT_OF_SERVICE" {
		return "", errors.NewBadRequestError("status must be ACTIVE, MAINTENANCE or OUT_OF_SERVICE")
	}
	code, err := s.dockCode(s.GetDB(ctx), facilityID, dockID)
	if err != nil {
		return "", err
	}
	return code, errors.RepositoryError(s.GetDB(ctx).Exec("UPDATE docks SET dock_status = ? WHERE dock_id = ?",
		status, dockID).Error)
}
