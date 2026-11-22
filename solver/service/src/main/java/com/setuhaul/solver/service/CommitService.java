package com.setuhaul.solver.service;

import io.quarkus.logging.Log;

import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;

import javax.sql.DataSource;

/**
 * Writes a solved run's bookings to appointments (plan of record, "Commit rules"):
 * <ol>
 * <li>check the run with the independent validator (db/validate);</li>
 * <li>in one transaction, serialised per facility, re-read the touched bookings and slots and reject the run if
 * anything it was planned on changed since its as-of time;</li>
 * <li>cancel the bookings the run moves or releases, then insert the new ones as CONFIRMED / SCHEDULING_TOOL.
 * A unique violation means another writer took a slot first: roll back and ask for a re-plan;</li>
 * <li>mark the run COMMITTED.</li>
 * </ol>
 * Kept bookings, flagged trucks and trucks in the dock are never written.
 */
@ApplicationScoped
public class CommitService {

    static final String ACTIVE = "('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS')";

    /** A truck's current booking is not the one the run was planned on. */
    private static final String CHANGED_BOOKINGS = """
            SELECT i.shipment_id, i.original_slot_id, a.slot_id
            FROM solver_plan_item i
            LEFT JOIN appointments a ON a.shipment_id = i.shipment_id AND a.is_current = 1
                 AND a.appointment_status IN %s
            WHERE i.run_id = ? AND i.allocation_status IN ('ALLOCATED', 'UNASSIGNED')
              AND a.slot_id IS DISTINCT FROM i.original_slot_id
            ORDER BY i.shipment_id
            """.formatted(ACTIVE);

    /** A target slot is taken by a booking this commit does not cancel, or is no longer OPEN. */
    private static final String TAKEN_SLOTS = """
            SELECT i.shipment_id, i.slot_id, 'booked by ' || a.shipment_id AS why
            FROM solver_plan_item i
            JOIN appointments a ON a.slot_id = i.slot_id AND a.appointment_status IN %1$s
            WHERE i.run_id = ? AND i.booking_action IN ('NEW', 'MOVED')
              AND NOT (a.is_current = 1 AND a.shipment_id IN (
                    SELECT shipment_id FROM solver_plan_item
                    WHERE run_id = ? AND booking_action IN ('MOVED', 'RELEASED')))
            UNION ALL
            SELECT i.shipment_id, i.slot_id, 'slot is ' || sl.slot_status
            FROM solver_plan_item i JOIN appointment_slots sl ON sl.slot_id = i.slot_id
            WHERE i.run_id = ? AND i.booking_action IN ('NEW', 'MOVED') AND sl.slot_status <> 'OPEN'
            UNION ALL
            SELECT i.shipment_id, i.slot_id, 'dock is ' || d.dock_status
            FROM solver_plan_item i JOIN docks d ON d.dock_id = i.dock_id
            WHERE i.run_id = ? AND i.booking_action IN ('NEW', 'MOVED') AND d.dock_status <> 'ACTIVE'
            ORDER BY 1
            """.formatted(ACTIVE);

    /** Inputs that arrived after the run's as-of time (ETA updates, GPS, check-ins, dock events, new requests, exceptions). */
    private static final String NEWER_INPUTS = """
            SELECT 'ETA update for ' || e.shipment_id FROM eta_updates e
            JOIN shipments s ON s.shipment_id = e.shipment_id
            WHERE s.destination_facility_id = ? AND e.created_at > ? AND e.created_at <= ?
            UNION ALL
            SELECT 'GPS estimate for ' || g.shipment_id FROM gps_eta_estimates g
            JOIN shipments s ON s.shipment_id = g.shipment_id
            WHERE s.destination_facility_id = ? AND g.computed_at > ? AND g.computed_at <= ?
            UNION ALL
            SELECT 'gate check-in of ' || fc.shipment_id FROM facility_checkins fc
            WHERE fc.facility_id = ?
              AND ((fc.gate_in_ts > ? AND fc.gate_in_ts <= ?) OR (fc.dock_in_ts > ? AND fc.dock_in_ts <= ?)
                   OR (fc.unload_end_ts > ? AND fc.unload_end_ts <= ?))
            UNION ALL
            SELECT 'dock event on ' || d.dock_code FROM dock_status_events ev
            JOIN docks d ON d.dock_id = ev.dock_id
            WHERE d.facility_id = ? AND ev.created_at > ? AND ev.created_at <= ?
            UNION ALL
            SELECT 'new truck request ' || s.shipment_id FROM shipments s
            WHERE s.destination_facility_id = ? AND s.created_at > ? AND s.created_at <= ?
            UNION ALL
            SELECT 'driver exception for ' || x.shipment_id FROM driver_exceptions x
            JOIN shipments s ON s.shipment_id = x.shipment_id
            WHERE s.destination_facility_id = ? AND x.reported_at > ? AND x.reported_at <= ?
            """;

    @Inject
    DataSource dataSource;

    @Inject
    RunRepository runs;

    @Inject
    PlanValidator validator;

    public Api.CommitResult commit(String runId) throws SQLException {
        Api.Run run = runs.find(runId).orElseThrow(() -> new NotFound("no run " + runId));
        if (!"SOLVED".equals(run.status())) {
            throw new Conflict("run " + runId + " is " + run.status() + "; only a SOLVED run can be committed");
        }

        List<Api.Violation> violations;
        try (Connection c = dataSource.getConnection()) {
            violations = validator.validate(c, runId);
        }
        if (!violations.isEmpty()) {
            String outcome = "INVALID: " + violations.stream().map(Api.Violation::checkId).distinct()
                    .collect(Collectors.joining(", "));
            runs.reject(runId, outcome, violations);
            Log.warnf("run %s rejected by the validator: %s", runId, violations);
            return new Api.CommitResult(runId, "REJECTED", outcome, false, 0, 0, violations);
        }

        try (Connection c = dataSource.getConnection()) {
            c.setAutoCommit(false);
            try {
                lockFacility(c, run.facilityId());
                if (!"SOLVED".equals(status(c, runId))) {
                    c.rollback();
                    throw new Conflict("run " + runId + " was committed or rejected meanwhile");
                }
                OffsetDateTime now = now(c);
                List<String> stale = staleReasons(c, run, now);
                if (!stale.isEmpty()) {
                    c.rollback();
                    String outcome = "STALE: " + String.join("; ", stale.subList(0, Math.min(5, stale.size())))
                            + (stale.size() > 5 ? "; and " + (stale.size() - 5) + " more" : "");
                    runs.reject(runId, outcome, List.of());
                    Log.infof("run %s is stale: %s", runId, outcome);
                    return new Api.CommitResult(runId, "REJECTED", outcome, true, 0, 0, List.of());
                }
                int[] counts = write(c, run, now);
                String outcome = "%d booked, %d cancelled".formatted(counts[0], counts[1]);
                try (PreparedStatement ps = c.prepareStatement("""
                        UPDATE solver_run SET status = 'COMMITTED', committed_at = ?, outcome = ? WHERE run_id = ?
                        """)) {
                    ps.setObject(1, now);
                    ps.setString(2, outcome);
                    ps.setString(3, runId);
                    ps.executeUpdate();
                }
                c.commit();
                Log.infof("run %s committed: %s", runId, outcome);
                return new Api.CommitResult(runId, "COMMITTED", outcome, false, counts[0], counts[1], List.of());
            } catch (SQLException e) {
                c.rollback();
                if ("23505".equals(e.getSQLState())) {
                    String outcome = "SLOT_TAKEN: another booking took a slot first";
                    runs.reject(runId, outcome, List.of());
                    Log.infof("run %s lost a slot race: %s", runId, e.getMessage());
                    return new Api.CommitResult(runId, "REJECTED", outcome, true, 0, 0, List.of());
                }
                throw e;
            } catch (RuntimeException e) {
                c.rollback();
                throw e;
            }
        }
    }

    static void lockFacility(Connection c, String facilityId) throws SQLException {
        try (PreparedStatement ps = c.prepareStatement("SELECT pg_advisory_xact_lock(hashtext(?))")) {
            ps.setString(1, "setuhaul.bookings:" + facilityId);
            ps.execute();
        }
    }

    static OffsetDateTime now(Connection c) throws SQLException {
        try (PreparedStatement ps = c.prepareStatement("SELECT setuhaul_now()"); ResultSet rs = ps.executeQuery()) {
            rs.next();
            return rs.getObject(1, OffsetDateTime.class);
        }
    }

    private static String status(Connection c, String runId) throws SQLException {
        try (PreparedStatement ps = c.prepareStatement("SELECT status FROM solver_run WHERE run_id = ? FOR UPDATE")) {
            ps.setString(1, runId);
            try (ResultSet rs = ps.executeQuery()) {
                return rs.next() ? rs.getString(1) : null;
            }
        }
    }

    private static List<String> staleReasons(Connection c, Api.Run run, OffsetDateTime now) throws SQLException {
        Set<String> out = new LinkedHashSet<>();
        try (PreparedStatement ps = c.prepareStatement(CHANGED_BOOKINGS)) {
            ps.setString(1, run.runId());
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    out.add("booking of " + rs.getString(1) + " changed (" + rs.getString(2) + " -> " + rs.getString(3) + ")");
                }
            }
        }
        try (PreparedStatement ps = c.prepareStatement(TAKEN_SLOTS)) {
            for (int i = 1; i <= 4; i++) {
                ps.setString(i, run.runId());
            }
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    out.add(rs.getString(2) + " for " + rs.getString(1) + " " + rs.getString(3));
                }
            }
        }
        try (PreparedStatement ps = c.prepareStatement(NEWER_INPUTS)) {
            int i = 1;
            // Six sources: facility then the (as_of, now] window; check-ins test three timestamps.
            for (int source = 0; source < 6; source++) {
                ps.setString(i++, run.facilityId());
                int windows = source == 2 ? 3 : 1;
                for (int w = 0; w < windows; w++) {
                    ps.setObject(i++, run.asOf());
                    ps.setObject(i++, now);
                }
            }
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    out.add(rs.getString(1) + " after " + run.asOf().toLocalTime());
                }
            }
        }
        return new ArrayList<>(out);
    }

    /** Cancels then inserts. Returns {booked, cancelled}. */
    private static int[] write(Connection c, Api.Run run, OffsetDateTime now) throws SQLException {
        Map<String, String> cancelledByShipment = new HashMap<>();
        try (PreparedStatement ps = c.prepareStatement("""
                UPDATE appointments
                SET appointment_status = 'CANCELLED', is_current = 0, cancelled_at = ?, cancellation_reason = ?,
                    updated_at = ?
                WHERE shipment_id = ? AND is_current = 1 AND appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED')
                RETURNING appointment_id
                """)) {
            for (Api.Item it : run.items()) {
                if (!"MOVED".equals(it.bookingAction()) && !"RELEASED".equals(it.bookingAction())) {
                    continue;
                }
                String why = it.reasonCode() != null ? it.reasonCode() : it.bookingAction();
                ps.setObject(1, now);
                ps.setString(2, "re-planned by " + run.runId() + ": " + why);
                ps.setObject(3, now);
                ps.setString(4, it.shipmentId());
                try (ResultSet rs = ps.executeQuery()) {
                    if (rs.next()) {
                        cancelledByShipment.put(it.shipmentId(), rs.getString(1));
                    }
                }
            }
        }
        int booked = 0;
        try (PreparedStatement ins = c.prepareStatement("""
                INSERT INTO appointments (appointment_id, shipment_id, slot_id, appointment_status, booking_source,
                    is_current, booked_at, confirmed_at, replaced_appointment_id, warehouse_confirmation_ref, updated_at)
                VALUES (?, ?, ?, 'CONFIRMED', 'SCHEDULING_TOOL', 1, ?, ?, ?, ?, ?)
                """);
                PreparedStatement link = c.prepareStatement(
                        "UPDATE solver_plan_item SET appointment_id = ? WHERE run_id = ? AND shipment_id = ?")) {
            for (Api.Item it : run.items()) {
                if (!"ALLOCATED".equals(it.allocationStatus())
                        || !("NEW".equals(it.bookingAction()) || "MOVED".equals(it.bookingAction()))) {
                    continue;
                }
                String id = "APT-" + UUID.randomUUID().toString().substring(0, 13).toUpperCase();
                ins.setString(1, id);
                ins.setString(2, it.shipmentId());
                ins.setString(3, it.slotId());
                ins.setObject(4, now);
                ins.setObject(5, now);
                ins.setString(6, cancelledByShipment.get(it.shipmentId()));
                ins.setString(7, run.runId());
                ins.setObject(8, now);
                ins.executeUpdate();
                link.setString(1, id);
                link.setString(2, run.runId());
                link.setString(3, it.shipmentId());
                link.executeUpdate();
                booked++;
            }
        }
        // The change feed the driver agent reads: one row per booking created, moved or released.
        try (PreparedStatement feed = c.prepareStatement("""
                INSERT INTO booking_changes (facility_id, run_id, shipment_id, action, from_slot_id, to_slot_id,
                    reason_code, detail, changed_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                """)) {
            for (Api.Item it : run.items()) {
                String action = it.bookingAction();
                boolean placed = "ALLOCATED".equals(it.allocationStatus()) && ("NEW".equals(action) || "MOVED".equals(action));
                boolean released = "RELEASED".equals(action) && cancelledByShipment.containsKey(it.shipmentId());
                if (!placed && !released) {
                    continue;
                }
                feed.setString(1, run.facilityId());
                feed.setString(2, run.runId());
                feed.setString(3, it.shipmentId());
                feed.setString(4, action);
                feed.setString(5, it.originalSlotId());
                feed.setString(6, placed ? it.slotId() : null);
                feed.setString(7, it.reasonCode());
                feed.setString(8, it.detail());
                feed.setObject(9, now);
                feed.addBatch();
            }
            feed.executeBatch();
        }
        return new int[] {booked, cancelledByShipment.size()};
    }

    /** No such run: 404. */
    public static class NotFound extends RuntimeException {
        public NotFound(String message) {
            super(message);
        }
    }

    /** The run is not in a state that allows the request: 409. */
    public static class Conflict extends RuntimeException {
        public Conflict(String message) {
            super(message);
        }
    }
}
