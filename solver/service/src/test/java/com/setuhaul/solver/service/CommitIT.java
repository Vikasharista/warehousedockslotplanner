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
