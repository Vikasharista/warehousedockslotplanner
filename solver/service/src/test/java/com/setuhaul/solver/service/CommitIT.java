package com.setuhaul.solver.service;

import io.quarkus.test.junit.QuarkusTest;
import io.quarkus.test.junit.TestProfile;
import io.restassured.http.ContentType;

import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.util.List;
import java.util.Map;

import jakarta.inject.Inject;

import javax.sql.DataSource;

import static io.restassured.RestAssured.given;
import static org.assertj.core.api.Assertions.assertThat;
import static org.hamcrest.Matchers.equalTo;
import static org.hamcrest.Matchers.startsWith;

/**
 * The commit path and the triggers against a private copy of the seeded snapshot. Tests run in order on a
 * clock that only moves forward, as the real one does.
 */
@QuarkusTest
@TestProfile(CloneDatabase.Profile.class)
@EnabledIfEnvironmentVariable(named = "SETUHAUL_TEST_JDBC_URL", matches = ".+")
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class CommitIT {

    static final String FACILITY = "FAC-JAI-01";

    @Inject
    DataSource dataSource;

    @Inject
    TriggerScanner triggers;

    @Test
    @Order(1)
    void replanCommitsValidBookingsAndASecondReplanChangesNothing() throws Exception {
        clock("2026-08-04T09:55:00+05:30");
        Map<String, Object> first = replan("MANUAL");
        assertThat(first).containsEntry("status", "COMMITTED");
        String runId = (String) first.get("finalRunId");

        // Every allocation the run made or moved is now the shipment's one current CONFIRMED booking.
        List<Map<String, Object>> rows = query("""
                SELECT i.shipment_id, i.booking_action, a.appointment_status, a.booking_source, a.slot_id = i.slot_id AS same
                FROM solver_plan_item i JOIN appointments a ON a.appointment_id = i.appointment_id
                WHERE i.run_id = ?
                """, runId);
        assertThat(rows).isNotEmpty().allSatisfy(r -> {
            assertThat(r).containsEntry("appointment_status", "CONFIRMED").containsEntry("booking_source", "SCHEDULING_TOOL")
                    .containsEntry("same", true);
            assertThat(r.get("booking_action")).isIn("NEW", "MOVED");
        });
        // Moved bookings point back at the booking they replace, which is cancelled and no longer current.
        assertThat(query("""
                SELECT old.appointment_status, old.is_current FROM solver_plan_item i
                JOIN appointments a ON a.appointment_id = i.appointment_id
                JOIN appointments old ON old.appointment_id = a.replaced_appointment_id
                WHERE i.run_id = ? AND i.booking_action = 'MOVED'
                """, runId)).isNotEmpty().allSatisfy(r -> assertThat(r)
                .containsEntry("appointment_status", "CANCELLED").containsEntry("is_current", 0));
        // Flagged weight breaches and the truck in the dock are never written.
        assertThat(query("SELECT appointment_id FROM solver_plan_item WHERE run_id = ? AND allocation_status <> 'ALLOCATED'"
                + " AND appointment_id IS NOT NULL", runId)).isEmpty();

        // The agent's change feed got one row per booking written or released, in this commit.
        assertThat(count("SELECT count(*) FROM booking_changes WHERE run_id = '" + runId + "'"))
                .isEqualTo(count("SELECT count(*) FROM solver_plan_item WHERE run_id = '" + runId
                        + "' AND booking_action IN ('NEW', 'MOVED', 'RELEASED')"));
        assertThat(count("SELECT count(*) FROM booking_changes WHERE run_id = '" + runId
                + "' AND action <> 'RELEASED' AND to_slot_id IS NULL")).isZero();

        clock("2026-08-04T09:57:00+05:30");
        Map<String, Object> second = replan("SCHEDULED");
        assertThat(second).containsEntry("status", "COMMITTED");
        assertThat(attempt(second, 0)).containsEntry("written", 0).containsEntry("cancelled", 0);
    }

    @Test
    @Order(2)
    void aRunPlannedBeforeNewerInputsIsStale() throws Exception {
        clock("2026-08-04T10:05:00+05:30");
        String runId = solve("2026-08-04T10:05:00+05:30");
        exec("""
                INSERT INTO eta_updates (eta_update_id, shipment_id, source_type, declared_eta_ts, confidence_code, created_at)
                VALUES ('ETA-IT-STALE', 'SHP1018', 'DRIVER_DECLARED', '2026-08-04T15:00:00+05:30', 'HIGH',
                        '2026-08-04T10:06:00+05:30')
                """);
        clock("2026-08-04T10:07:00+05:30");
        given().when().post("/runs/" + runId + "/commit").then().statusCode(200)
                .body("status", equalTo("REJECTED")).body("retry", equalTo(true))
                .body("outcome", startsWith("STALE: ETA update for SHP1018"));
        assertThat(status(runId)).isEqualTo("REJECTED");
        // Only a SOLVED run can be committed.
        given().when().post("/runs/" + runId + "/commit").then().statusCode(409);
        given().when().post("/runs/RUN-none/commit").then().statusCode(404);
    }

    @Test
    @Order(3)
    void aSlotTakenAfterSolvingIsStaleAndTheReplanRetries() throws Exception {
        clock("2026-08-04T10:15:00+05:30");
        exec("DELETE FROM eta_updates WHERE eta_update_id = 'ETA-IT-STALE'");
        // Free one truck so the plan books a slot for it, then take that slot by hand before committing.
        exec("""
                UPDATE appointments SET appointment_status = 'CANCELLED', is_current = 0, cancelled_at = setuhaul_now(),
                    updated_at = setuhaul_now()
                WHERE shipment_id = 'SHP1020' AND is_current = 1
                """);
        String runId = solve("2026-08-04T10:15:00+05:30");
        String slot = (String) query("SELECT slot_id FROM solver_plan_item WHERE run_id = ? AND shipment_id = 'SHP1020'",
                runId).get(0).get("slot_id");
        assertThat(slot).isNotNull();
        exec("""
                INSERT INTO appointments (appointment_id, shipment_id, slot_id, appointment_status, booking_source, is_current,
                    booked_at, updated_at)
                VALUES ('APT-IT-RACE', 'SHP1019', '%s', 'PENDING_CONFIRMATION', 'DRIVER_CHAT', 0, setuhaul_now(), setuhaul_now())
                """.formatted(slot));
        given().when().post("/runs/" + runId + "/commit").then().statusCode(200)
                .body("status", equalTo("REJECTED")).body("retry", equalTo(true))
                .body("outcome", startsWith("STALE: " + slot + " for SHP1020 booked by SHP1019"));

        // A fresh re-plan sees the hold, steers around it and commits.
        Map<String, Object> r = replan("MANUAL");
        assertThat(r).containsEntry("status", "COMMITTED");
        String placed = (String) query("SELECT slot_id FROM solver_plan_item WHERE run_id = ? AND shipment_id = 'SHP1020'",
                r.get("finalRunId")).get(0).get("slot_id");
        assertThat(placed).isNotEqualTo(slot);
        exec("DELETE FROM appointments WHERE appointment_id = 'APT-IT-RACE'");
    }

    @Test
    @Order(4)
    void aRunTheValidatorRejectsIsNeverWritten() throws Exception {
        clock("2026-08-04T10:25:00+05:30");
        String runId = solve("2026-08-04T10:25:00+05:30");
        // Corrupt the stored plan: two trucks on one slot.
        exec("""
                UPDATE solver_plan_item SET slot_id = (SELECT slot_id FROM solver_plan_item WHERE run_id = '%1$s'
                        AND allocation_status = 'ALLOCATED' ORDER BY shipment_id LIMIT 1),
                    dock_id = (SELECT dock_id FROM solver_plan_item WHERE run_id = '%1$s'
                        AND allocation_status = 'ALLOCATED' ORDER BY shipment_id LIMIT 1),
                    booking_action = 'MOVED'
                WHERE run_id = '%1$s' AND shipment_id = (SELECT shipment_id FROM solver_plan_item WHERE run_id = '%1$s'
                        AND allocation_status = 'ALLOCATED' ORDER BY shipment_id OFFSET 1 LIMIT 1)
                """.formatted(runId));
        long before = count("SELECT count(*) FROM appointments");
        given().when().post("/runs/" + runId + "/commit").then().statusCode(200)
                .body("status", equalTo("REJECTED")).body("retry", equalTo(false))
                .body("outcome", startsWith("INVALID: ")).body("violations.checkId", org.hamcrest.Matchers.hasItem("V2"));
        assertThat(count("SELECT count(*) FROM appointments")).isEqualTo(before);
        given().when().get("/runs/" + runId).then().statusCode(200)
                .body("status", equalTo("REJECTED")).body("violations.size()", org.hamcrest.Matchers.greaterThan(0));
    }

    @Test
    @Order(5)
    void triggersFollowTheClockEtasAndCheckins() throws Exception {
        clock("2026-08-04T10:40:00+05:30");
        assertThat(replan("MANUAL")).containsEntry("status", "COMMITTED");
        assertThat(triggers.decide(FACILITY)).isEmpty();

        // A booked truck's ETA slips 50 minutes: an ETA trigger, long before the schedule is due.
        clock("2026-08-04T10:43:00+05:30");
        String shipment = (String) query("""
                SELECT i.shipment_id FROM solver_plan_item i JOIN solver_run r USING (run_id)
                WHERE r.status = 'COMMITTED' AND i.allocation_status = 'ALLOCATED' AND i.eta_basis_ts IS NOT NULL
                  AND i.shipment_id <> 'SHP1009'
                ORDER BY r.committed_at DESC, i.shipment_id LIMIT 1
                """).get(0).get("shipment_id");
        exec("""
                INSERT INTO eta_updates (eta_update_id, shipment_id, source_type, declared_eta_ts, confidence_code, created_at)
                SELECT 'ETA-IT-SLIP', i.shipment_id, 'DRIVER_DECLARED', i.eta_basis_ts + interval '50 minutes', 'HIGH',
                       setuhaul_now()
                FROM solver_plan_item i JOIN solver_run r USING (run_id)
                WHERE r.status = 'COMMITTED' AND i.shipment_id = '%s' ORDER BY r.committed_at DESC LIMIT 1
                """.formatted(shipment));
        var eta = triggers.decide(FACILITY).orElseThrow();
        assertThat(eta.trigger()).isEqualTo("ETA_DELAY");
        assertThat(eta.detail()).contains(shipment + " +50 min");

        // A truck at the gate outranks the ETA trigger.
        exec(CHECKIN_SHP1009);
        var checkin = triggers.decide(FACILITY).orElseThrow();
        assertThat(checkin.trigger()).isEqualTo("CHECKIN");
        assertThat(checkin.detail()).contains("SHP1009");

        // One re-plan covers both; afterwards nothing is due until the schedule.
        assertThat(replan("CHECKIN")).containsEntry("status", "COMMITTED");
        clock("2026-08-04T10:46:00+05:30");
        assertThat(triggers.decide(FACILITY)).isEmpty();
        clock("2026-08-04T11:01:00+05:30");
        assertThat(triggers.decide(FACILITY)).get().extracting(TriggerScanner.Decision::trigger).isEqualTo("SCHEDULED");
    }

    @Test
    @Order(6)
    void aRequestExistsOnlyFromWhenItWasMadeAndTriggersARePlan() throws Exception {
        clock("2026-08-04T11:10:00+05:30");
        exec("""
                INSERT INTO shipments (shipment_id, order_reference, carrier_id, driver_id, vehicle_id, origin_name,
                    origin_city, destination_facility_id, customer_name, product_category, load_weight_kg, pallet_count,
                    required_dock_type, temperature_control_required, priority_code, planned_departure_ts,
                    actual_departure_ts, original_eta_ts, latest_eta_ts, expected_unload_min, current_status,
                    created_at, updated_at)
                SELECT 'SHP-IT-NEW', 'ORD-IT-NEW', carrier_id, driver_id, vehicle_id, origin_name, 'Dausa',
                    destination_facility_id, customer_name, product_category, load_weight_kg, pallet_count,
                    required_dock_type, temperature_control_required, 'CRITICAL', '2026-08-04T12:30:00+05:30',
                    NULL, '2026-08-04T14:00:00+05:30', NULL, expected_unload_min, 'ASSIGNED',
                    '2026-08-04T11:20:00+05:30', '2026-08-04T11:20:00+05:30'
                FROM shipments WHERE shipment_id = 'SHP1018'
                """);
        // Before 11:20 the request does not exist: not planned, no NEW_REQUEST trigger.
        String before = solve("2026-08-04T11:10:00+05:30");
        assertThat(query("SELECT 1 FROM solver_plan_item WHERE run_id = ? AND shipment_id = 'SHP-IT-NEW'", before)).isEmpty();
        assertThat(triggers.decide(FACILITY).map(TriggerScanner.Decision::trigger)).isNotEqualTo(java.util.Optional.of("NEW_REQUEST"));
        assertThat(replan("SCHEDULED")).containsEntry("status", "COMMITTED");

        // Once the clock passes 11:20 it fires NEW_REQUEST, and the re-plan books it.
        clock("2026-08-04T11:23:00+05:30");
        var due = triggers.decide(FACILITY).orElseThrow();
        assertThat(due.trigger()).isEqualTo("NEW_REQUEST");
        assertThat(due.detail()).contains("1 new: SHP-IT-NEW");
        Map<String, Object> r = replan("NEW_REQUEST");
        assertThat(r).containsEntry("status", "COMMITTED");
        assertThat(query("""
                SELECT a.appointment_status FROM solver_plan_item i JOIN appointments a ON a.appointment_id = i.appointment_id
                WHERE i.run_id = ? AND i.shipment_id = 'SHP-IT-NEW'
                """, r.get("finalRunId"))).extracting(m -> m.get("appointment_status")).containsExactly("CONFIRMED");
        assertThat(query("SELECT run_trigger FROM solver_run WHERE run_id = ?", r.get("finalRunId")))
                .extracting(m -> m.get("run_trigger")).containsExactly("NEW_REQUEST");
    }

    @Test
    @Order(7)
    void aCriticalRequestBumpsOneLowBookingAndAHighOneDoesNot() throws Exception {
        clock("2026-08-04T11:30:00+05:30");
        // Book every free reefer slot between 14:00 and 18:00 for LOW reefer trucks (copies of SHP1015).
        exec("""
                WITH free AS (
                    SELECT sl.slot_id, row_number() OVER (ORDER BY sl.slot_start_ts) AS n
                    FROM appointment_slots sl JOIN docks d ON d.dock_id = sl.dock_id
                    WHERE d.facility_id = 'FAC-JAI-01' AND d.dock_type = 'REEFER' AND sl.slot_status = 'OPEN'
                      AND sl.slot_start_ts >= '2026-08-04T14:00:00+05:30' AND sl.slot_start_ts < '2026-08-04T18:00:00+05:30'
                      AND NOT EXISTS (SELECT 1 FROM appointments a WHERE a.slot_id = sl.slot_id
                                      AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS'))
                ), trucks AS (
                    INSERT INTO shipments (shipment_id, order_reference, carrier_id, driver_id, vehicle_id, origin_name,
                        origin_city, destination_facility_id, customer_name, product_category, load_weight_kg, pallet_count,
                        required_dock_type, temperature_control_required, priority_code, planned_departure_ts,
                        actual_departure_ts, original_eta_ts, latest_eta_ts, expected_unload_min, current_status,
                        created_at, updated_at)
                    SELECT 'SHP-IT-LOW' || f.n, 'ORD-IT-LOW' || f.n, s.carrier_id, s.driver_id, s.vehicle_id, s.origin_name,
                        'Dausa', s.destination_facility_id, s.customer_name, s.product_category, s.load_weight_kg,
                        s.pallet_count, 'REEFER', 1, 'LOW', '2026-08-04T10:30:00+05:30', NULL,
                        '2026-08-04T12:00:00+05:30', NULL, 60, 'ASSIGNED', '2026-08-04T09:00:00+05:30',
                        '2026-08-04T09:00:00+05:30'
                    FROM free f CROSS JOIN shipments s WHERE s.shipment_id = 'SHP1015'
                    RETURNING shipment_id
                )
                INSERT INTO appointments (appointment_id, shipment_id, slot_id, appointment_status, booking_source,
                    is_current, booked_at, confirmed_at, updated_at)
                SELECT 'APT-IT-LOW' || f.n, 'SHP-IT-LOW' || f.n, f.slot_id, 'CONFIRMED', 'PLANNER', 1,
                       '2026-08-04T09:00:00+05:30', '2026-08-04T09:00:00+05:30', '2026-08-04T09:00:00+05:30'
                FROM free f
                """);
        assertThat(count("SELECT count(*) FROM shipments WHERE shipment_id LIKE 'SHP-IT-LOW%'")).isGreaterThan(0);
        // A HIGH reefer request ready at 14:00: every reefer slot it could use is booked.
        exec("""
                INSERT INTO shipments (shipment_id, order_reference, carrier_id, driver_id, vehicle_id, origin_name,
                    origin_city, destination_facility_id, customer_name, product_category, load_weight_kg, pallet_count,
                    required_dock_type, temperature_control_required, priority_code, planned_departure_ts,
                    actual_departure_ts, original_eta_ts, latest_eta_ts, expected_unload_min, current_status,
                    created_at, updated_at)
                SELECT 'SHP-IT-CRIT', 'ORD-IT-CRIT', carrier_id, driver_id, vehicle_id, origin_name, 'Dausa',
                    destination_facility_id, customer_name, product_category, load_weight_kg, pallet_count, 'REEFER', 1,
                    'HIGH', '2026-08-04T12:00:00+05:30', NULL, '2026-08-04T13:30:00+05:30', NULL, 60, 'ASSIGNED',
                    '2026-08-04T11:00:00+05:30', '2026-08-04T11:00:00+05:30'
                FROM shipments WHERE shipment_id = 'SHP1015'
                """);
        String high = (String) replan("MANUAL").get("finalRunId");
        assertThat(query("SELECT allocation_status FROM solver_plan_item WHERE run_id = ? AND shipment_id = 'SHP-IT-CRIT'",
                high)).extracting(m -> m.get("allocation_status")).containsExactly("UNASSIGNED");
        assertThat(query("SELECT 1 FROM solver_plan_item WHERE run_id = ? AND reason_code = 'BUMPED_FOR_CRITICAL'", high))
                .isEmpty();

        // As CRITICAL it takes exactly one LOW booking's reefer slot; that truck is re-planned and its booking cancelled.
        exec("UPDATE shipments SET priority_code = 'CRITICAL' WHERE shipment_id = 'SHP-IT-CRIT'");
        clock("2026-08-04T11:33:00+05:30");
        Map<String, Object> r = replan("MANUAL");
        assertThat(r).containsEntry("status", "COMMITTED");
        String runId = (String) r.get("finalRunId");
        List<Map<String, Object>> crit = query("""
                SELECT i.dock_id, i.slot_id, a.appointment_status FROM solver_plan_item i
                JOIN appointments a ON a.appointment_id = i.appointment_id
                WHERE i.run_id = ? AND i.shipment_id = 'SHP-IT-CRIT'
                """, runId);
        assertThat(crit).hasSize(1);
        assertThat(crit.get(0)).containsEntry("appointment_status", "CONFIRMED");
        List<Map<String, Object>> bumped = query("""
                SELECT shipment_id, original_slot_id, booking_action, detail FROM solver_plan_item
                WHERE run_id = ? AND reason_code = 'BUMPED_FOR_CRITICAL'
                """, runId);
        assertThat(bumped).hasSize(1);
        assertThat((String) bumped.get(0).get("shipment_id")).startsWith("SHP-IT-LOW");
        assertThat(bumped.get(0)).containsEntry("original_slot_id", crit.get(0).get("slot_id"));
        assertThat((String) bumped.get(0).get("detail")).contains("CRITICAL SHP-IT-CRIT");
        assertThat(query("""
                SELECT cancellation_reason FROM appointments WHERE shipment_id = ? AND slot_id = ?
                """, bumped.get(0).get("shipment_id"), bumped.get(0).get("original_slot_id")))
                .extracting(m -> (String) m.get("cancellation_reason"))
                .singleElement().asString().contains("BUMPED_FOR_CRITICAL");
    }

    @Test
    @Order(8)
    void aDockTakenOutOfServiceAfterSolvingMakesTheRunStale() throws Exception {
        clock("2026-08-04T11:50:00+05:30");
        exec("UPDATE appointments SET appointment_status = 'CANCELLED', is_current = 0 WHERE shipment_id = 'SHP1020' AND is_current = 1");
        String runId = solve("2026-08-04T11:50:00+05:30");
        String dock = (String) query("SELECT dock_id FROM solver_plan_item WHERE run_id = ? AND shipment_id = 'SHP1020'", runId)
                .get(0).get("dock_id");
        exec("UPDATE docks SET dock_status = 'OUT_OF_SERVICE' WHERE dock_id = '" + dock + "'");
        long before = count("SELECT count(*) FROM appointments");
        try {
            // Refused by the validator (V4) or, if the dock changed after validation, by the stale check.
            given().when().post("/runs/" + runId + "/commit").then().statusCode(200)
                    .body("status", equalTo("REJECTED"))
                    .body("outcome", org.hamcrest.Matchers.anyOf(org.hamcrest.Matchers.containsString("V4"),
                            org.hamcrest.Matchers.containsString("dock is OUT_OF_SERVICE")));
            assertThat(count("SELECT count(*) FROM appointments")).isEqualTo(before);
        } finally {
            exec("UPDATE docks SET dock_status = 'ACTIVE' WHERE dock_id = '" + dock + "'");
        }
    }

    static final String CHECKIN_SHP1009 = """
            INSERT INTO facility_checkins (checkin_id, shipment_id, facility_id, gate_in_ts, arrival_state, queue_state,
                updated_at)
            VALUES ('CHK-IT-1', 'SHP1009', 'FAC-JAI-01', setuhaul_now() - interval '1 minute', 'ON_TIME', 'NOT_QUEUED',
                    setuhaul_now())
            """;

    // --- helpers ---

    private Map<String, Object> replan(String trigger) {
        return given().contentType(ContentType.JSON)
                .body(Map.of("facilityId", FACILITY, "trigger", trigger))
                .when().post("/replan").then().statusCode(200).extract().as(Map.class);
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> attempt(Map<String, Object> replan, int i) {
        return ((List<Map<String, Object>>) replan.get("attempts")).get(i);
    }

    private String solve(String asOf) {
        return given().contentType(ContentType.JSON)
                .body(Map.of("facilityId", FACILITY, "asOf", asOf, "solveSeconds", 2))
                .when().post("/solve").then().statusCode(201).body("status", equalTo("SOLVED"))
                .extract().path("runId");
    }

    /** Sets the shared clock to `at`, running at real speed from now. */
    private void clock(String at) throws Exception {
        exec("DELETE FROM app_clock; INSERT INTO app_clock (sim_anchor, real_anchor) VALUES ('" + at + "', now())");
    }

    private String status(String runId) throws Exception {
        return (String) query("SELECT status FROM solver_run WHERE run_id = ?", runId).get(0).get("status");
    }

    private long count(String sql) throws Exception {
        return ((Number) query(sql).get(0).get("count")).longValue();
    }

    private void exec(String sql) throws Exception {
        try (Connection c = dataSource.getConnection()) {
            c.setAutoCommit(true);
            c.createStatement().execute(sql);
        }
    }

    private List<Map<String, Object>> query(String sql, Object... args) throws Exception {
        try (Connection c = dataSource.getConnection(); PreparedStatement ps = c.prepareStatement(sql)) {
            for (int i = 0; i < args.length; i++) {
                ps.setObject(i + 1, args[i]);
            }
            try (ResultSet rs = ps.executeQuery()) {
                List<Map<String, Object>> out = new java.util.ArrayList<>();
                while (rs.next()) {
                    Map<String, Object> row = new java.util.HashMap<>();
                    for (int col = 1; col <= rs.getMetaData().getColumnCount(); col++) {
                        row.put(rs.getMetaData().getColumnLabel(col), rs.getObject(col));
                    }
                    out.add(row);
                }
                return out;
            }
        }
    }
}
