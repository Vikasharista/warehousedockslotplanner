package com.setuhaul.solver.load;

import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.domain.Compatibility;
import com.setuhaul.solver.domain.Dock;
import com.setuhaul.solver.domain.DockBlock;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.PlanWeights;
import com.setuhaul.solver.domain.ShipmentAssignment;
import com.setuhaul.solver.domain.ShipmentAssignment.BookingStatus;
import com.setuhaul.solver.domain.Slot;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.time.Instant;
import java.time.LocalDate;
import java.time.LocalTime;
import java.time.OffsetDateTime;
import java.time.ZoneId;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

/**
 * Reads one facility's state from Postgres, as it stood at {@code asOf}, into a {@link DockPlan}.
 *
 * <p>Reads run in one REPEATABLE READ, READ ONLY transaction, so the plan comes from a single consistent snapshot
 * and the loader can never write. Facts recorded after {@code asOf} are ignored: ETA updates and dock events by
 * {@code created_at}, bookings by {@code booked_at}, check-ins by {@code gate_in_ts} and {@code dock_in_ts}.
 * Shipment status itself is taken as stored, because the snapshot keeps no status history.
 */
public final class PostgresLoader {

    /**
     * One row per shipment with its effective ETA as of the given time, by the same rule as the v_effective_eta
     * view: the newest OPERATIONS_OVERRIDE wins; otherwise a GPS estimate (the later of it and the newest
     * DRIVER_DECLARED ETA while the driver's exception is open); otherwise the newest ETA update; otherwise
     * {@code original_eta_ts} (applied in Java). A stale GPS estimate counts as LOW confidence.
     */
    private static final String SHIPMENTS_SQL = """
            WITH p AS (SELECT CAST(? AS timestamptz) AS as_of)
            SELECT s.shipment_id, s.vehicle_id, s.priority_code, s.required_dock_type,
                   s.temperature_control_required, s.load_weight_kg, s.expected_unload_min, s.current_status,
                   s.original_eta_ts, s.planned_departure_ts, vt.refrigerated_flag,
                   CASE
                       WHEN ovr.declared_eta_ts IS NOT NULL THEN ovr.declared_eta_ts
                       WHEN gps.eta_ts IS NOT NULL AND exc.is_open AND drv.declared_eta_ts IS NOT NULL
                           THEN GREATEST(gps.eta_ts, drv.declared_eta_ts)
                       WHEN gps.eta_ts IS NOT NULL THEN gps.eta_ts
                       ELSE upd.declared_eta_ts
                   END AS declared_eta_ts,
                   CASE
                       WHEN ovr.declared_eta_ts IS NOT NULL THEN ovr.confidence_code
                       WHEN gps.eta_ts IS NOT NULL THEN CASE WHEN gps.stale THEN 'LOW' ELSE gps.confidence_code END
                       ELSE upd.confidence_code
                   END AS confidence_code,
                   fc.gate_in_ts, fc.dock_in_ts, fc.unload_start_ts, fc.unload_end_ts, fc.actual_dock_id
            FROM shipments s
            CROSS JOIN p
            JOIN vehicles v ON v.vehicle_id = s.vehicle_id
            JOIN vehicle_types vt ON vt.vehicle_type_code = v.vehicle_type_code
            LEFT JOIN LATERAL (
                SELECT e.declared_eta_ts, e.confidence_code FROM eta_updates e
                WHERE e.shipment_id = s.shipment_id AND e.created_at <= p.as_of
                ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1
            ) upd ON TRUE
            LEFT JOIN LATERAL (
                SELECT e.declared_eta_ts, e.confidence_code FROM eta_updates e
                WHERE e.shipment_id = s.shipment_id AND e.created_at <= p.as_of
                  AND e.source_type = 'OPERATIONS_OVERRIDE'
                ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1
            ) ovr ON TRUE
            LEFT JOIN LATERAL (
                SELECT e.declared_eta_ts FROM eta_updates e
                WHERE e.shipment_id = s.shipment_id AND e.created_at <= p.as_of
                  AND e.source_type = 'DRIVER_DECLARED'
                ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1
            ) drv ON TRUE
            LEFT JOIN LATERAL (
                SELECT g.eta_ts, g.confidence_code, g.stale FROM gps_eta_estimates g
                WHERE g.shipment_id = s.shipment_id AND g.computed_at <= p.as_of
                ORDER BY g.computed_at DESC, g.estimate_id DESC LIMIT 1
            ) gps ON TRUE
            LEFT JOIN LATERAL (
                SELECT TRUE AS is_open FROM driver_exceptions x
                WHERE x.shipment_id = s.shipment_id AND x.reported_at <= p.as_of
                  AND x.exception_status NOT IN ('RESOLVED', 'CANCELLED', 'DUPLICATE')
                LIMIT 1
            ) exc ON TRUE
            LEFT JOIN facility_checkins fc ON fc.shipment_id = s.shipment_id
            WHERE s.destination_facility_id = ?
              AND s.current_status NOT IN ('COMPLETED', 'CANCELLED')
            ORDER BY s.shipment_id
            """;

    private static final String BOOKINGS_SQL = """
            SELECT a.shipment_id, a.slot_id, a.appointment_status, a.booked_at, sl.dock_id
            FROM appointments a
            JOIN appointment_slots sl ON sl.slot_id = a.slot_id
            WHERE sl.facility_id = ?
              AND a.is_current = 1
              AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS')
              AND a.booked_at <= ?
            """;

    /**
     * Live driver holds on another slot by a shipment that still has its current booking (is_current = 0 until
     * the driver confirms). The held slot is not free for anyone else while the hold lasts.
     */
    private static final String SIDE_HOLDS_SQL = """
            SELECT a.shipment_id, a.slot_id, a.booked_at, sl.dock_id, sl.slot_start_ts, sl.slot_end_ts
            FROM appointments a
            JOIN appointment_slots sl ON sl.slot_id = a.slot_id
            WHERE sl.facility_id = ?
              AND a.is_current = 0
              AND a.appointment_status = 'PENDING_CONFIRMATION'
              AND a.booked_at <= ?
            """;

    private final SolverSettings settings;

    public PostgresLoader(SolverSettings settings) {
        this.settings = settings;
    }

    public LoadResult load(Connection conn, String facilityId, OffsetDateTime asOf) throws SQLException {
        boolean autoCommit = conn.getAutoCommit();
        conn.setAutoCommit(false);
        try (Statement st = conn.createStatement()) {
            st.execute("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY");
            return read(conn, facilityId, asOf);
        } finally {
            conn.rollback();
            conn.setAutoCommit(autoCommit);
        }
    }

    private LoadResult read(Connection conn, String facilityId, OffsetDateTime asOfTs) throws SQLException {
        // Inputs are read up to the exact as-of time; planning starts at the next whole minute, so nothing is
        // planned before the moment the plan is made.
        long asOf = Math.floorDiv(asOfTs.toEpochSecond() + 59, 60);
        long horizonEnd = asOf + settings.horizonHours() * 60L;

        // Facility, hours and rules.
        ZoneId zone;
        LocalTime open;
        LocalTime close;
        int facilityDefaultUnload;
        try (PreparedStatement ps = conn.prepareStatement(
                "SELECT timezone, open_time, close_time, default_unload_min, active_flag FROM facilities WHERE facility_id = ?")) {
            ps.setString(1, facilityId);
            try (ResultSet rs = ps.executeQuery()) {
                if (!rs.next()) {
                    throw new IllegalArgumentException("unknown facility " + facilityId);
                }
                if (rs.getInt("active_flag") != 1) {
                    throw new IllegalArgumentException("facility " + facilityId + " is not active");
                }
                zone = ZoneId.of(rs.getString("timezone"));
                open = rs.getObject("open_time", LocalTime.class);
                close = rs.getObject("close_time", LocalTime.class);
                facilityDefaultUnload = rs.getInt("default_unload_min");
            }
        }
        Map<String, String> rules = new HashMap<>();
        LocalDate asOfDate = asOfTs.atZoneSameInstant(zone).toLocalDate();
        try (PreparedStatement ps = conn.prepareStatement("""
                SELECT rule_type, rule_value FROM facility_rules
                WHERE facility_id = ? AND active_flag = 1
                  AND (effective_from IS NULL OR effective_from <= ?)
                  AND (effective_to IS NULL OR effective_to >= ?)
                """)) {
            ps.setString(1, facilityId);
            ps.setObject(2, asOfDate);
            ps.setObject(3, asOfDate);
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    rules.put(rs.getString(1), rs.getString(2).trim());
                }
            }
        }
        LocalTime lastNewStart = Optional.ofNullable(rules.get("LAST_NEW_START_TIME")).map(LocalTime::parse).orElse(null);
        Integer heavyAboveKg = Optional.ofNullable(rules.get("HEAVY_DOCK_REQUIRED_KG")).map(Integer::valueOf).orElse(null);

        // Docks.
        Map<String, Dock> docks = new LinkedHashMap<>();
        try (PreparedStatement ps = conn.prepareStatement("""
                SELECT dock_id, dock_code, dock_type, supports_refrigerated, max_vehicle_weight_kg, dock_status
                FROM docks WHERE facility_id = ? ORDER BY dock_code
                """)) {
            ps.setString(1, facilityId);
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    docks.put(rs.getString("dock_id"), new Dock(rs.getString("dock_id"), rs.getString("dock_code"),
                            Dock.Type.valueOf(rs.getString("dock_type")), rs.getInt("supports_refrigerated") == 1,
                            rs.getInt("max_vehicle_weight_kg"), "ACTIVE".equals(rs.getString("dock_status"))));
                }
            }
        }

        // Dock events known at asOf become blocks; a later REOPENED event on the same dock ends a block early.
        List<DockBlock> blocks = new ArrayList<>(eventBlocks(conn, facilityId, asOfTs, docks, horizonEnd));
        List<DockBlock> eventBlocks = List.copyOf(blocks);

        // Facility closed between closing time and the next opening, on every day the horizon touches.
        LocalDate lastDate = Instant.ofEpochSecond(horizonEnd * 60).atZone(zone).toLocalDate();
        for (LocalDate d = asOfDate.minusDays(1); !d.isAfter(lastDate); d = d.plusDays(1)) {
            long closedFrom = minutes(d.atTime(close).atZone(zone).toOffsetDateTime());
            long closedTo = minutes(d.plusDays(1).atTime(open).atZone(zone).toOffsetDateTime());
            if (closedTo > closedFrom) {
                for (Dock dock : docks.values()) {
                    blocks.add(new DockBlock(dock, closedFrom, closedTo, DockBlock.Kind.FACILITY_CLOSED, "closed"));
                }
            }
        }

        // Slots that matter to the horizon. Not-OPEN slots become blocks too.
        Map<String, Slot> slots = new LinkedHashMap<>();
        try (PreparedStatement ps = conn.prepareStatement("""
                SELECT slot_id, dock_id, slot_start_ts, slot_end_ts, slot_status
                FROM appointment_slots
                WHERE facility_id = ? AND slot_end_ts > ? AND slot_start_ts < ?
                ORDER BY slot_start_ts, dock_id
                """)) {
            ps.setString(1, facilityId);
            ps.setObject(2, asOfTs);
            ps.setObject(3, instantTs(horizonEnd));
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    Dock dock = docks.get(rs.getString("dock_id"));
                    long start = minutes(rs.getObject("slot_start_ts", OffsetDateTime.class));
                    long end = minutes(rs.getObject("slot_end_ts", OffsetDateTime.class));
                    boolean open_ = "OPEN".equals(rs.getString("slot_status"));
                    boolean hitsEvent = eventBlocks.stream().anyMatch(b -> b.dock().equals(dock) && b.overlaps(start, end));
                    boolean afterLast = lastNewStart != null
                            && Instant.ofEpochSecond(start * 60).atZone(zone).toLocalTime().isAfter(lastNewStart);
                    String id = rs.getString("slot_id");
                    slots.put(id, new Slot(id, dock, start, end, open_ && dock.isActive() && !hitsEvent, afterLast));
                    if (!open_) {
                        blocks.add(new DockBlock(dock, start, end, DockBlock.Kind.BLOCKED_SLOT, id));
                    }
                }
            }
        }

        // Current bookings known at asOf.
        Map<String, Booking> bookings = new HashMap<>();
        try (PreparedStatement ps = conn.prepareStatement(BOOKINGS_SQL)) {
            ps.setString(1, facilityId);
            ps.setObject(2, asOfTs);
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    bookings.put(rs.getString("shipment_id"), new Booking(rs.getString("slot_id"),
                            rs.getString("appointment_status"), minutes(rs.getObject("booked_at", OffsetDateTime.class)),
                            docks.get(rs.getString("dock_id"))));
                }
            }
        }

        try (PreparedStatement ps = conn.prepareStatement(SIDE_HOLDS_SQL)) {
            ps.setString(1, facilityId);
            ps.setObject(2, asOfTs);
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    long bookedAt = minutes(rs.getObject("booked_at", OffsetDateTime.class));
                    Dock dock = docks.get(rs.getString("dock_id"));
                    if (dock != null && bookedAt + settings.holdExpiryMin() > asOf) {
                        blocks.add(new DockBlock(dock, minutes(rs.getObject("slot_start_ts", OffsetDateTime.class)),
                                minutes(rs.getObject("slot_end_ts", OffsetDateTime.class)), DockBlock.Kind.HELD_SLOT,
                                rs.getString("shipment_id")));
                    }
                }
            }
        }

        Map<String, List<long[]>> departuresByVehicle = vehicleDepartures(conn);

        List<LoadResult.Note> flagged = new ArrayList<>();
        List<LoadResult.Note> inDock = new ArrayList<>();
        List<LoadResult.Note> outsideHorizon = new ArrayList<>();
        List<Truck> trucks = new ArrayList<>();

        try (PreparedStatement ps = conn.prepareStatement(SHIPMENTS_SQL)) {
            ps.setObject(1, asOfTs);
            ps.setString(2, facilityId);
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    trucks.add(Truck.from(rs));
                }
            }
        }

        // Trucks physically in a dock occupy it; they are not planned.
        for (Truck t : trucks) {
            if (t.dockInTs != null && t.dockInTs <= asOf && (t.unloadEndTs == null || t.unloadEndTs > asOf)) {
                Dock dock = docks.get(t.actualDockId);
                long started = t.unloadStartTs != null ? t.unloadStartTs : t.dockInTs;
                int unload = unloadMin(t.expectedUnloadMin, facilityDefaultUnload);
                long expectedEnd = started + unload;
                long end = expectedEnd > asOf ? expectedEnd : asOf + settings.inDockOverrunMin();
                if (dock != null) {
                    blocks.add(new DockBlock(dock, started, end, DockBlock.Kind.TRUCK_IN_DOCK, t.id));
                }
                inDock.add(new LoadResult.Note(t.id, expectedEnd > asOf ? "IN_DOCK" : "IN_DOCK_OVERRUN",
                        "in " + (dock == null ? t.actualDockId : dock.getCode()) + " since " + clock(started, zone)
                                + ", assumed free at " + clock(end, zone), t.actualDockId, null));
                t.handled = true;
            }
        }

        // Bookings over the dock's weight limit are flagged and left exactly where they are.
        for (Truck t : trucks) {
            Booking b = bookings.get(t.id);
            if (t.handled || b == null || b.dock == null || t.loadWeightKg <= b.dock.getMaxVehicleWeightKg()) {
                continue;
            }
            flagged.add(new LoadResult.Note(t.id, "WEIGHT_BREACH", t.loadWeightKg + " kg booked on "
                    + b.dock.getCode() + " (max " + b.dock.getMaxVehicleWeightKg() + " kg); coordinator to decide",
                    b.dock.getId(), b.slotId));
            Slot s = slots.get(b.slotId);
            if (s != null) {
                long end = s.getStartMin() + Math.max(unloadMin(t.expectedUnloadMin, facilityDefaultUnload), s.lengthMin());
                blocks.add(new DockBlock(s.getDock(), s.getStartMin(), end, DockBlock.Kind.FLAGGED_BOOKING, t.id));
            }
            t.handled = true;
        }

        List<DockBlock> staticBlocks = List.copyOf(blocks);
        List<Slot> futureSlots = slots.values().stream()
                .filter(s -> s.getStartMin() >= asOf && s.getStartMin() < horizonEnd)
                .toList();

        List<ShipmentAssignment> assignments = new ArrayList<>();
        Map<String, String> emptyReasons = new LinkedHashMap<>();
        for (Truck t : trucks) {
            if (t.handled) {
                continue;
            }
            boolean checkedIn = t.gateInTs != null && t.gateInTs <= asOf;
            long eta = t.declaredEtaTs != null ? t.declaredEtaTs : t.originalEtaTs;
            String confidence = t.confidence != null ? t.confidence : "HIGH";
            long ready = checkedIn
                    ? Math.max(asOf, t.gateInTs)
                    : Math.max(asOf, eta + settings.gateBufferMin()
                            + ("LOW".equals(confidence) ? settings.lowConfidenceMarginMin() : 0));
            if (ready >= horizonEnd) {
                outsideHorizon.add(new LoadResult.Note(t.id, "READY_AFTER_HORIZON",
                        "earliest ready " + clock(ready, zone) + ", horizon ends " + clock(horizonEnd, zone)));
                continue;
            }

            boolean reefer = t.temperatureControl || t.refrigeratedVehicle || "REEFER".equals(t.requiredDockType);
            Dock.Type needed = switch (t.requiredDockType) {
                case "STANDARD" -> Dock.Type.STANDARD;
                case "HEAVY" -> Dock.Type.HEAVY;
                default -> null;
            };
            if (heavyAboveKg != null && t.loadWeightKg > heavyAboveKg) {
                needed = Dock.Type.HEAVY;
            }
            int unload = unloadMin(t.expectedUnloadMin, facilityDefaultUnload);

            ShipmentAssignment a = new ShipmentAssignment(t.id, t.priority, settings.priorityWeight(t.priority),
                    needed, reefer, t.loadWeightKg, unload, ready);
            a.setVehicleId(t.vehicleId);
            a.setEtaConfidence(checkedIn ? "AT_GATE" : confidence);
            a.setEtaMin(checkedIn ? null : eta);
            a.setNextPickupMin(nextPickup(departuresByVehicle, t));

            List<Slot> candidates = futureSlots.stream()
                    .filter(s -> Compatibility.violation(a, s) == null)
                    .filter(s -> staticBlocks.stream().noneMatch(blk -> blk.dock().equals(s.getDock())
                            && blk.overlaps(s.getStartMin(), s.getStartMin() + Math.max(unload, s.lengthMin()))))
                    .toList();
            a.setCandidateSlots(candidates);
            if (candidates.isEmpty()) {
                emptyReasons.put(t.id, diagnose(a, futureSlots, staticBlocks));
            }

            Booking b = bookings.get(t.id);
            if (b != null) {
                applyBooking(a, b, slots.get(b.slotId), asOf, staticBlocks);
            }
            assignments.add(a);
        }
        releaseOverlappingPins(assignments);

        DockPlan plan = new DockPlan(facilityId, Instant.ofEpochSecond(asOf * 60), List.copyOf(docks.values()),
                List.copyOf(slots.values()), List.copyOf(blocks),
                new PlanWeights(settings.unforcedMovePenalty(), settings.forcedMovePenalty()), assignments);
        return new LoadResult(plan, zone, Instant.ofEpochSecond(asOf * 60), Instant.ofEpochSecond(horizonEnd * 60),
                flagged, inDock, outsideHorizon, emptyReasons);
    }

    /** Keep a booking (pinned) when it is still valid; otherwise record why it has to be re-planned. */
    private void applyBooking(ShipmentAssignment a, Booking b, Slot slot, long asOf, List<DockBlock> blocks) {
        BookingStatus status = "PENDING_CONFIRMATION".equals(b.status) ? BookingStatus.PENDING_CONFIRMATION
                : BookingStatus.CONFIRMED;
        String reason;
        if (status == BookingStatus.PENDING_CONFIRMATION && b.bookedAt + settings.holdExpiryMin() <= asOf) {
            reason = "HOLD_EXPIRED";
        } else if (slot == null || slot.getStartMin() < asOf) {
            reason = "SLOT_ALREADY_STARTED";
        } else if (a.canUse(slot)) {
            reason = null;
        } else {
            Compatibility.Violation v = Compatibility.violation(a, slot);
            if (v == Compatibility.Violation.H1_BEFORE_EARLIEST_READY) {
                reason = "TRUCK_CANNOT_MAKE_SLOT";
            } else if (v != null) {
                reason = v.name();
            } else {
                reason = blocks.stream()
                        .filter(blk -> blk.dock().equals(slot.getDock())
                                && blk.overlaps(slot.getStartMin(), slot.getStartMin() + Math.max(a.getUnloadMin(), slot.lengthMin())))
                        .map(blk -> "BLOCKED_BY_" + blk.kind())
                        .findFirst().orElse("NOT_A_CANDIDATE");
            }
        }
        a.setOriginalBooking(b.slotId, slot, status, reason);
        if (reason == null) {
            a.setSlot(slot);
            a.setPinned(status == BookingStatus.PENDING_CONFIRMATION || settings.pinFeasibleConfirmed());
        }
    }

    /** Two kept bookings on one dock may still overlap (a long unload); the later one is released. */
    private static void releaseOverlappingPins(List<ShipmentAssignment> assignments) {
        List<ShipmentAssignment> pinned = assignments.stream()
                .filter(ShipmentAssignment::isPinned)
                .sorted(Comparator.comparingLong(ShipmentAssignment::startMin))
                .toList();
        Map<Dock, Long> busyUntil = new HashMap<>();
        for (ShipmentAssignment a : pinned) {
            Long until = busyUntil.get(a.dock());
            if (until != null && a.startMin() < until) {
                a.setOriginalBooking(a.getOriginalSlotId(), a.getOriginalSlot(), a.getOriginalStatus(), "OVERLAPS_EARLIER_BOOKING");
                a.setPinned(false);
                a.setSlot(null);
            } else {
                busyUntil.put(a.dock(), a.endMin());
            }
        }
    }

    /** Why a truck has no usable slot, checking the rules from the most basic to the most specific. */
    static String diagnose(ShipmentAssignment a, List<Slot> futureSlots, List<DockBlock> blocks) {
        List<Slot> dockFits = futureSlots.stream().filter(s -> Compatibility.dockViolation(a, s.getDock()) == null).toList();
        if (dockFits.isEmpty()) {
            return "NO_COMPATIBLE_DOCK";
        }
        List<Slot> afterReady = dockFits.stream().filter(s -> s.getStartMin() >= a.getEarliestReadyMin()).toList();
        if (afterReady.isEmpty()) {
            return "NO_SLOT_AFTER_READY_IN_HORIZON";
        }
        List<Slot> open = afterReady.stream()
                .filter(s -> s.isAvailable() && s.getDock().isActive())
                .filter(s -> blocks.stream().noneMatch(b -> b.dock().equals(s.getDock())
                        && b.overlaps(s.getStartMin(), s.getStartMin() + Math.max(a.getUnloadMin(), s.lengthMin()))))
                .toList();
        if (open.isEmpty()) {
            return "COMPATIBLE_SLOTS_BLOCKED";
        }
        return "AFTER_LAST_START_NEEDS_APPROVAL";
    }

    private List<DockBlock> eventBlocks(Connection conn, String facilityId, OffsetDateTime asOfTs, Map<String, Dock> docks,
            long horizonEnd) throws SQLException {
        record Event(String id, Dock dock, String type, long start, Long end) {
        }
        List<Event> events = new ArrayList<>();
        try (PreparedStatement ps = conn.prepareStatement("""
                SELECT e.dock_event_id, e.dock_id, e.event_type, e.event_start_ts, e.event_end_ts
                FROM dock_status_events e JOIN docks d ON d.dock_id = e.dock_id
                WHERE d.facility_id = ? AND e.created_at <= ?
                ORDER BY e.dock_id, e.event_start_ts
                """)) {
            ps.setString(1, facilityId);
            ps.setObject(2, asOfTs);
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    OffsetDateTime end = rs.getObject("event_end_ts", OffsetDateTime.class);
                    events.add(new Event(rs.getString(1), docks.get(rs.getString(2)), rs.getString(3),
                            minutes(rs.getObject("event_start_ts", OffsetDateTime.class)), end == null ? null : minutes(end)));
                }
            }
        }
        List<DockBlock> out = new ArrayList<>();
        long openEnded = horizonEnd + 24 * 60;
        for (Event e : events) {
            if ("REOPENED".equals(e.type) || e.dock == null) {
                continue;
            }
            long end = e.end != null ? e.end : openEnded;
            for (Event r : events) {
                if ("REOPENED".equals(r.type) && r.dock == e.dock && r.start > e.start && r.start < end) {
                    end = r.start;
                }
            }
            if (end > e.start) {
                out.add(new DockBlock(e.dock, e.start, end, DockBlock.Kind.DOCK_EVENT, e.id + " " + e.type));
            }
        }
        return out;
    }

    private static Map<String, List<long[]>> vehicleDepartures(Connection conn) throws SQLException {
        Map<String, List<long[]>> byVehicle = new HashMap<>();
        try (Statement st = conn.createStatement();
             ResultSet rs = st.executeQuery("""
                     SELECT vehicle_id, planned_departure_ts FROM shipments
                     WHERE current_status <> 'CANCELLED'
                     """)) {
            while (rs.next()) {
                byVehicle.computeIfAbsent(rs.getString(1), k -> new ArrayList<>())
                        .add(new long[] {minutes(rs.getObject(2, OffsetDateTime.class))});
            }
        }
        return byVehicle;
    }

    /** The vehicle's next planned departure after this shipment's own departure, or null. */
    private static Long nextPickup(Map<String, List<long[]>> byVehicle, Truck t) {
        return byVehicle.getOrDefault(t.vehicleId, List.of()).stream()
                .mapToLong(x -> x[0])
                .filter(dep -> dep > t.plannedDepartureTs)
                .min()
                .stream().boxed().findFirst().orElse(null);
    }

    private int unloadMin(Integer shipmentMin, int facilityDefault) {
        if (shipmentMin != null && shipmentMin > 0) {
            return shipmentMin;
        }
        return facilityDefault > 0 ? facilityDefault : settings.defaultUnloadMin();
    }

    static long minutes(OffsetDateTime ts) {
        return Math.floorDiv(ts.toEpochSecond(), 60);
    }

    private static OffsetDateTime instantTs(long epochMinutes) {
        return OffsetDateTime.ofInstant(Instant.ofEpochSecond(epochMinutes * 60), java.time.ZoneOffset.UTC);
    }

    static String clock(long epochMinutes, ZoneId zone) {
        return Instant.ofEpochSecond(epochMinutes * 60).atZone(zone).toLocalDateTime()
                .format(java.time.format.DateTimeFormatter.ofPattern("MM-dd HH:mm"));
    }

    private record Booking(String slotId, String status, long bookedAt, Dock dock) {
    }

    /** One shipment row as read. */
    private static final class Truck {
        String id;
        String vehicleId;
        String priority;
        String requiredDockType;
        boolean temperatureControl;
        boolean refrigeratedVehicle;
        int loadWeightKg;
        Integer expectedUnloadMin;
        long originalEtaTs;
        long plannedDepartureTs;
        Long declaredEtaTs;
        String confidence;
        Long gateInTs;
        Long dockInTs;
        Long unloadStartTs;
        Long unloadEndTs;
        String actualDockId;
        boolean handled;

        static Truck from(ResultSet rs) throws SQLException {
            Truck t = new Truck();
            t.id = rs.getString("shipment_id");
            t.vehicleId = rs.getString("vehicle_id");
            t.priority = rs.getString("priority_code");
            t.requiredDockType = rs.getString("required_dock_type");
            t.temperatureControl = rs.getInt("temperature_control_required") == 1;
            t.refrigeratedVehicle = rs.getInt("refrigerated_flag") == 1;
            t.loadWeightKg = rs.getInt("load_weight_kg");
            t.expectedUnloadMin = (Integer) rs.getObject("expected_unload_min");
            t.originalEtaTs = minutes(rs.getObject("original_eta_ts", OffsetDateTime.class));
            t.plannedDepartureTs = minutes(rs.getObject("planned_departure_ts", OffsetDateTime.class));
            t.declaredEtaTs = optMinutes(rs, "declared_eta_ts");
            t.confidence = rs.getString("confidence_code");
            t.gateInTs = optMinutes(rs, "gate_in_ts");
            t.dockInTs = optMinutes(rs, "dock_in_ts");
            t.unloadStartTs = optMinutes(rs, "unload_start_ts");
            t.unloadEndTs = optMinutes(rs, "unload_end_ts");
            t.actualDockId = rs.getString("actual_dock_id");
            return t;
        }

        private static Long optMinutes(ResultSet rs, String col) throws SQLException {
            OffsetDateTime v = rs.getObject(col, OffsetDateTime.class);
            return v == null ? null : minutes(v);
        }
    }
}
