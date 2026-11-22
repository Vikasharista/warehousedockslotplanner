package com.setuhaul.solver;

import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.ShipmentAssignment;
import com.setuhaul.solver.load.LoadResult;
import com.setuhaul.solver.load.PostgresLoader;
import com.setuhaul.solver.report.Kpis;
import com.setuhaul.solver.solve.DockSolver;

import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.time.Instant;
import java.time.OffsetDateTime;
import java.time.ZoneId;
import java.util.Map;
import java.util.function.Function;
import java.util.stream.Collectors;

import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

/**
 * Solves the seeded snapshot. Needs SETUHAUL_TEST_JDBC_URL pointing at a database seeded by
 * {@code python -m setuhaul_jobs.seed}; skipped otherwise. Runs stop after a fixed step count, so results repeat.
 */
class SnapshotSolveIT {

    static final String FACILITY = "FAC-JAI-01";
    static final OffsetDateTime MORNING = OffsetDateTime.parse("2026-08-04T09:55:00+05:30");
    static final OffsetDateTime EVENING = OffsetDateTime.parse("2026-08-04T16:30:00+05:30");
    static final int STEPS = 3_000;

    static String url;
    static final SolverSettings SETTINGS = SolverSettings.defaults();

    @BeforeAll
    static void needsDatabase() {
        url = System.getenv("SETUHAUL_TEST_JDBC_URL");
        assumeTrue(url != null && !url.isBlank(), "SETUHAUL_TEST_JDBC_URL is not set");
    }

    static LoadResult load(OffsetDateTime asOf) throws Exception {
        try (Connection c = DriverManager.getConnection(url)) {
            return new PostgresLoader(SETTINGS).load(c, FACILITY, asOf);
        }
    }

    static DockPlan solve(LoadResult load) {
        return DockSolver.solve(load.plan(), SETTINGS, DockSolver.stepLimited(STEPS));
    }

    static Map<String, ShipmentAssignment> byId(DockPlan plan) {
        return plan.getAssignments().stream()
                .collect(Collectors.toMap(ShipmentAssignment::getShipmentId, Function.identity()));
    }

    static String clock(long epochMinutes) {
        return Instant.ofEpochSecond(epochMinutes * 60).atZone(ZoneId.of("Asia/Kolkata")).toLocalTime().toString();
    }

    @Test
    void morningPlanIsFeasibleAndRespectsEveryRule() throws Exception {
        LoadResult load = load(MORNING);
        DockPlan solved = solve(load);
        Map<String, ShipmentAssignment> a = byId(solved);

        assertThat(solved.getScore().isFeasible()).as("score %s", solved.getScore()).isTrue();

        // Weight breaches are flagged and left alone; the overrunning truck in D2 is not planned.
        assertThat(load.flagged()).extracting(LoadResult.Note::shipmentId).containsExactlyInAnyOrder("SHP1005", "SHP1014");
        assertThat(load.flagged()).extracting(LoadResult.Note::reason).containsOnly("WEIGHT_BREACH");
        assertThat(load.inDock()).extracting(LoadResult.Note::shipmentId).containsExactly("SHP1002");
        assertThat(a).doesNotContainKeys("SHP1002", "SHP1005", "SHP1014", "SHP1008", "SHP1019");

        // The expired driver hold and the missed 09:00 bookings are released for re-planning.
        assertThat(a.get("SHP1013").getReleaseReason()).isEqualTo("HOLD_EXPIRED");
        assertThat(a.get("SHP1003").getReleaseReason()).isEqualTo("SLOT_ALREADY_STARTED");
        assertThat(a.get("SHP1004").getReleaseReason()).isEqualTo("SLOT_ALREADY_STARTED");

        // Reefer and heavy loads land on the right docks; nothing starts before it can.
        assertThat(a.get("SHP1010").getSlot().getDock().getCode()).isEqualTo("D5");
        assertThat(a.get("SHP1011").getSlot().getDock().getCode()).isEqualTo("D6");
        assertThat(a.get("SHP1016").getSlot().getDock().getCode()).isEqualTo("D6");
        for (ShipmentAssignment s : solved.getAssignments()) {
            if (s.getSlot() != null) {
                assertThat(s.startMin()).as(s.getShipmentId()).isGreaterThanOrEqualTo(s.getEarliestReadyMin());
                if (s.getSlot().getDock().getCode().equals("D3")) {
                    assertThat(clock(s.startMin())).as("D3 is broken until 13:00").isGreaterThanOrEqualTo("13:00");
                }
            }
        }

        // The reefer load cannot be placed: the reefer dock is under maintenance from 18:00.
        assertThat(a.get("SHP1015").getSlot()).isNull();
        assertThat(load.emptyCandidateReasons()).containsEntry("SHP1015", "COMPATIBLE_SLOTS_BLOCKED");
        // Its ETA update arrives only at 16:25, so at 09:55 the original 17:30 ETA applies.
        assertThat(clock(a.get("SHP1015").getEarliestReadyMin())).isEqualTo("18:00");

        Kpis k = Kpis.of(solved, load.asOf().getEpochSecond() / 60, load.horizonEnd().getEpochSecond() / 60);
        assertThat(k.priorityInversions()).isZero();
        assertThat(k.unforcedMoves()).isZero();
        assertThat(k.unassigned()).isEqualTo(1);
    }

    @Test
    void eveningPlanSeesTheLateEtaUpdate() throws Exception {
        LoadResult load = load(EVENING);
        DockPlan solved = solve(load);
        Map<String, ShipmentAssignment> a = byId(solved);

        assertThat(solved.getScore().isFeasible()).isTrue();
        ShipmentAssignment reefer = a.get("SHP1015");
        assertThat(clock(reefer.getEarliestReadyMin())).as("18:30 ETA + 30 min buffer").isEqualTo("19:00");
        assertThat(reefer.getReleaseReason()).isEqualTo("TRUCK_CANNOT_MAKE_SLOT");
        assertThat(reefer.getSlot()).isNull();
        assertThat(load.emptyCandidateReasons()).containsEntry("SHP1015", "COMPATIBLE_SLOTS_BLOCKED");
    }

    @Test
    void gpsEstimatesAndOverridesCountOnlyAsOfTheirTime() throws Exception {
        // SHP1013 (from Dausa, on no lane) gets hand-made GPS estimates; the rows are removed afterwards.
        try (Connection c = DriverManager.getConnection(url)) {
            c.setAutoCommit(true);
            try {
                c.createStatement().execute("""
                        INSERT INTO gps_eta_estimates (shipment_id, vehicle_id, lane_id, remaining_km, eta_ts,
                            confidence_code, computed_at)
                        SELECT 'SHP1013', vehicle_id, 'L1-DEL-JAI', 50, '2026-08-04T12:00:00+05:30'::timestamptz, 'HIGH',
                               '2026-08-04T09:00:00+05:30'::timestamptz FROM shipments WHERE shipment_id = 'SHP1013'
                        UNION ALL
                        SELECT 'SHP1013', vehicle_id, 'L1-DEL-JAI', 10, '2026-08-04T15:00:00+05:30'::timestamptz, 'HIGH',
                               '2026-08-04T10:30:00+05:30'::timestamptz FROM shipments WHERE shipment_id = 'SHP1013'
                        """);
                // 09:00 estimate (12:00) applies; the 10:30 one does not exist yet at 09:55.
                // The driver's declared 11:00 is earlier, so it cannot win even while the exception is open.
                assertThat(clock(byId(load(MORNING).plan()).get("SHP1013").getEarliestReadyMin())).isEqualTo("12:30");
                // By 11:00 the later estimate applies.
                assertThat(clock(byId(load(OffsetDateTime.parse("2026-08-04T11:00:00+05:30")).plan())
                        .get("SHP1013").getEarliestReadyMin())).isEqualTo("15:30");

                c.createStatement().execute("""
                        INSERT INTO eta_updates (eta_update_id, shipment_id, source_type, declared_eta_ts,
                            confidence_code, created_at)
                        VALUES ('ETA-IT-OVR', 'SHP1013', 'OPERATIONS_OVERRIDE', '2026-08-04T14:00:00+05:30', 'HIGH',
                                '2026-08-04T09:50:00+05:30')
                        """);
                // An operations override wins over GPS.
                assertThat(clock(byId(load(MORNING).plan()).get("SHP1013").getEarliestReadyMin())).isEqualTo("14:30");
            } finally {
                c.createStatement().execute("DELETE FROM gps_eta_estimates WHERE shipment_id = 'SHP1013'");
                c.createStatement().execute("DELETE FROM eta_updates WHERE eta_update_id = 'ETA-IT-OVR'");
            }
        }
    }

    @Test
    void sameInputAndSeedGiveTheSamePlan() throws Exception {
        Map<String, String> first = slots(solve(load(MORNING)));
        Map<String, String> second = slots(solve(load(MORNING)));
        assertThat(second).isEqualTo(first);
    }

    @Test
    void loadingLeavesTheDatabaseUntouchedAndRestoresAutoCommit() throws Exception {
        try (Connection c = DriverManager.getConnection(url)) {
            long before = count(c);
            new PostgresLoader(SETTINGS).load(c, FACILITY, MORNING);
            assertThat(c.getAutoCommit()).isTrue();
            assertThat(count(c)).isEqualTo(before);
        }
    }

    private static long count(Connection c) throws Exception {
        try (ResultSet rs = c.createStatement().executeQuery("SELECT count(*) FROM appointments")) {
            rs.next();
            return rs.getLong(1);
        }
    }

    private static Map<String, String> slots(DockPlan plan) {
        return plan.getAssignments().stream().collect(Collectors.toMap(ShipmentAssignment::getShipmentId,
                s -> s.getSlot() == null ? "-" : s.getSlot().getId()));
    }

    @Test
    void aSilentGpsDeviceKeepsItsEtaButAddsTheLowConfidenceMargin() throws Exception {
        // SHP1009's last GPS estimate (12:00) was written stale: no ping for 30 minutes. Removed afterwards.
        try (Connection c = DriverManager.getConnection(url)) {
            c.setAutoCommit(true);
            try {
                c.createStatement().execute("""
                        INSERT INTO gps_eta_estimates (shipment_id, vehicle_id, lane_id, remaining_km, eta_ts,
                            confidence_code, stale, computed_at)
                        SELECT 'SHP1009', vehicle_id, 'L1-DEL-JAI', 80, '2026-08-04T12:00:00+05:30'::timestamptz, 'LOW',
                               true, '2026-08-04T09:50:00+05:30'::timestamptz FROM shipments WHERE shipment_id = 'SHP1009'
                        """);
                ShipmentAssignment a = byId(load(MORNING).plan()).get("SHP1009");
                assertThat(a.getEtaConfidence()).isEqualTo("LOW");
                assertThat(clock(a.getEtaMin())).as("the ETA holds").isEqualTo("12:00");
                assertThat(clock(a.getEarliestReadyMin())).as("12:00 + 30 min buffer + 15 min margin").isEqualTo("12:45");
            } finally {
                c.createStatement().execute("DELETE FROM gps_eta_estimates WHERE shipment_id = 'SHP1009' AND stale"
                        + " AND computed_at = '2026-08-04T09:50:00+05:30'");
            }
        }
    }
}
