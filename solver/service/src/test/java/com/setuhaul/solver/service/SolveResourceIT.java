package com.setuhaul.solver.service;

import io.quarkus.test.junit.QuarkusTest;
import io.restassured.http.ContentType;
import io.restassured.response.Response;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.condition.EnabledIfEnvironmentVariable;

import java.sql.Connection;
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
 * Runs the service against a seeded database (SETUHAUL_TEST_JDBC_URL); skipped without one.
 * Seed it with {@code python -m setuhaul_jobs.seed --apply-schema}.
 */
@QuarkusTest
@EnabledIfEnvironmentVariable(named = "SETUHAUL_TEST_JDBC_URL", matches = ".+")
class SolveResourceIT {

    static final String MORNING = """
            {"facilityId": "FAC-JAI-01", "asOf": "2026-08-04T09:55:00+05:30", "trigger": "ETA_DELAY", "solveSeconds": 2}
            """;

    @Inject
    DataSource dataSource;

    @Test
    void solveStoresAFeasibleRunAndReturnsIt() {
        Response r = given().contentType(ContentType.JSON).body(MORNING)
                .when().post("/solve")
                .then().statusCode(201)
                .header("Location", startsWith("http"))
                .body("status", equalTo("SOLVED"))
                .body("feasible", equalTo(true))
                .body("trigger", equalTo("ETA_DELAY"))
                .body("asOf", equalTo("2026-08-04T09:55:00+05:30"))
                .extract().response();

        String runId = r.path("runId");
        List<Map<String, Object>> items = r.path("items");
        assertThat(items).hasSize(17);
        assertThat(byShipment(items, "SHP1015")).containsEntry("allocationStatus", "UNASSIGNED")
                .containsEntry("reasonCode", "COMPATIBLE_SLOTS_BLOCKED").containsEntry("bookingAction", "RELEASED");
        assertThat(byShipment(items, "SHP1005")).containsEntry("allocationStatus", "FLAGGED")
                .containsEntry("reasonCode", "WEIGHT_BREACH").containsEntry("originalSlotId", "SLOT-JAI-030");
        assertThat(byShipment(items, "SHP1002")).containsEntry("allocationStatus", "IN_DOCK")
                .containsEntry("dockId", "DOCK-JAI-D2");
        assertThat(byShipment(items, "SHP1010")).containsEntry("allocationStatus", "ALLOCATED")
                .containsEntry("dockId", "DOCK-JAI-D5");
        assertThat((Integer) r.path("kpis.priorityInversions")).isZero();

        // The stored run reads back the same.
        given().when().get("/runs/" + runId)
                .then().statusCode(200)
                .body("runId", equalTo(runId))
                .body("items.size()", equalTo(17))
                .body("kpis.assigned", equalTo((Integer) r.path("kpis.assigned")));
    }

    @Test
    void solvingNeverWritesAppointments() throws Exception {
        String before = appointmentsFingerprint();
        given().contentType(ContentType.JSON).body(MORNING).when().post("/solve").then().statusCode(201);
        assertThat(appointmentsFingerprint()).isEqualTo(before);
    }

    @Test
    void unknownRunIs404() {
        given().when().get("/runs/RUN-does-not-exist").then().statusCode(404)
                .body("error", startsWith("no run"));
    }

    @Test
    void badRequestsAre400WithAMessage() {
        given().contentType(ContentType.JSON).body("{\"facilityId\": \"FAC-JAI-01\"}")
                .when().post("/solve").then().statusCode(400).body("error", startsWith("asOf is required"));
        given().contentType(ContentType.JSON)
                .body("{\"facilityId\": \"NOPE\", \"asOf\": \"2026-08-04T09:55:00+05:30\"}")
                .when().post("/solve").then().statusCode(400).body("error", equalTo("unknown facility NOPE"));
        given().contentType(ContentType.JSON)
                .body("{\"facilityId\": \"FAC-JAI-01\", \"asOf\": \"2026-08-04T09:55:00+05:30\", \"solveSeconds\": 600}")
                .when().post("/solve").then().statusCode(400).body("error", startsWith("solveSeconds must be"));
        given().contentType(ContentType.JSON)
                .body("{\"facilityId\": \"FAC-JAI-01\", \"asOf\": \"2026-08-04T09:55:00+05:30\", \"trigger\": \"WHENEVER\"}")
                .when().post("/solve").then().statusCode(400).body("error", startsWith("trigger must be"));
    }

    @Test
    void readinessReportsTheDatabase() {
        given().when().get("/q/health/ready").then().statusCode(200).body("status", equalTo("UP"));
    }

    private static Map<String, Object> byShipment(List<Map<String, Object>> items, String id) {
        return items.stream().filter(i -> id.equals(i.get("shipmentId"))).findFirst().orElseThrow();
    }

    private String appointmentsFingerprint() throws Exception {
        try (Connection c = dataSource.getConnection();
             ResultSet rs = c.createStatement().executeQuery(
                     "SELECT md5(string_agg(a::text, '|' ORDER BY appointment_id)) FROM appointments a")) {
            rs.next();
            return rs.getString(1);
        }
    }
}
