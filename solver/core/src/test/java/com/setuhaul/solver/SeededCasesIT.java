package com.setuhaul.solver;

import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.ShipmentAssignment;
import com.setuhaul.solver.load.LoadResult;
import com.setuhaul.solver.report.Kpis;

import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

import static com.setuhaul.solver.SnapshotSolveIT.MORNING;
import static com.setuhaul.solver.SnapshotSolveIT.byId;
import static com.setuhaul.solver.SnapshotSolveIT.clock;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

/**
 * The database guide's seeded cases (section 6) that concern planning, one test each, with the expected slot or
 * outcome written out, on the 09:55 snapshot. The plan is solved once, with a fixed step count so it repeats. The
 * guide's chat and e-mail cases are mapped in docs/verification.md. Needs SETUHAUL_TEST_JDBC_URL.
 */
class SeededCasesIT {

    static LoadResult load;
    static DockPlan plan;
    static Map<String, ShipmentAssignment> a;

    @BeforeAll
    static void solveOnce() throws Exception {
        String url = System.getenv("SETUHAUL_TEST_JDBC_URL");
        assumeTrue(url != null && !url.isBlank(), "SETUHAUL_TEST_JDBC_URL is not set");
        SnapshotSolveIT.url = url;
        load = SnapshotSolveIT.load(MORNING);
        plan = SnapshotSolveIT.solve(load);
        a = byId(plan);
        assertThat(plan.getScore().isFeasible()).isTrue();
    }

    static String slot(String shipment) {
        ShipmentAssignment s = a.get(shipment);
        return s.getSlot().getDock().getCode() + " " + clock(s.startMin());
    }

    @Test
    void normalAppointmentSHP1002IsInTheDockAndHoldsD2UntilItsUnloadEnds() {
        assertThat(load.inDock()).extracting(LoadResult.Note::shipmentId).containsExactly("SHP1002");
        assertThat(a).doesNotContainKey("SHP1002");
        // Unload overrun (DEVT003): no truck starts on D2 before the overrunning truck is assumed free (10:10).
        a.values().stream().filter(s -> s.getSlot() != null && s.getSlot().getDock().getCode().equals("D2"))
                .forEach(s -> assertThat(clock(s.startMin())).as(s.getShipmentId()).isGreaterThanOrEqualTo("10:10"));
    }

    @Test
    void earlyArrivalSHP1003TakesTheEarliestFreeSlotButDisplacesNobody() {
        assertThat(a.get("SHP1003").getReleaseReason()).isEqualTo("SLOT_ALREADY_STARTED");
        assertThat(clock(a.get("SHP1003").startMin())).isEqualTo("10:00");
        Kpis k = Kpis.of(plan, load.asOf().getEpochSecond() / 60, load.horizonEnd().getEpochSecond() / 60);
        assertThat(k.unforcedMoves()).as("an early truck never pushes a booked truck out").isZero();
    }

    @Test
    void lateArrivalAtTheYardSHP1004IsReplannedOnTheNextSlot() {
        assertThat(a.get("SHP1004").getReleaseReason()).isEqualTo("SLOT_ALREADY_STARTED");
        assertThat(clock(a.get("SHP1004").startMin())).isEqualTo("10:00");
    }

    @Test
    void lateEtaAndMultipleUpdatesSHP1006UseTheNewestEta() {
        ShipmentAssignment s = a.get("SHP1006");
        assertThat(s.getReleaseReason()).isEqualTo("TRUCK_CANNOT_MAKE_SLOT");
        assertThat(clock(s.getEtaMin())).as("revised 10:50 -> 11:20").isEqualTo("11:20");
        assertThat(clock(s.getEarliestReadyMin())).isEqualTo("11:50");
        assertThat(clock(s.startMin())).isEqualTo("12:00");
    }

    @Test
    void uncertainEtasSHP1013AndSHP1017GetTheLowConfidenceMargin() {
        for (String id : List.of("SHP1013", "SHP1017")) {
            ShipmentAssignment s = a.get(id);
            assertThat(s.getEtaConfidence()).as(id).isEqualTo("LOW");
            assertThat(s.getEarliestReadyMin() - s.getEtaMin()).as(id + ": 30 min buffer + 15 min margin").isEqualTo(45);
            assertThat(s.startMin()).as(id).isGreaterThanOrEqualTo(s.getEarliestReadyMin());
        }
    }

    @Test
    void dockBreakdownDEVT001KeepsD3EmptyUntil1300AndTheWeightBreachSHP1005IsFlagged() {
        assertThat(load.flagged()).extracting(LoadResult.Note::shipmentId).contains("SHP1005");
        a.values().stream().filter(s -> s.getSlot() != null && s.getSlot().getDock().getCode().equals("D3"))
                .forEach(s -> assertThat(clock(s.startMin())).as(s.getShipmentId()).isGreaterThanOrEqualTo("13:00"));
    }

    @Test
    void cancellationSHP1008FreesItsSlotForOthers() throws Exception {
        assertThat(a).doesNotContainKeys("SHP1008", "SHP1019");
        String freed = one("SELECT slot_id FROM appointments WHERE appointment_id = 'APT1008'");
        assertThat(plan.getSlots()).anySatisfy(s -> assertThat(s.getId()).isEqualTo(freed));
        assertThat(plan.getBlocks()).noneMatch(b -> freed.equals(b.reference()));
    }

    @Test
    void noShowSHP1018IsPlannedAgainAfterItsEta() {
        ShipmentAssignment s = a.get("SHP1018");
        assertThat(s.getSlot()).isNotNull();
        assertThat(s.startMin()).isGreaterThanOrEqualTo(s.getEarliestReadyMin());
        assertThat(slot("SHP1018")).isEqualTo("D4 13:00");
    }

    @Test
    void reeferHeavyAndUnloadDurationsSHP1010SHP1016SHP1011() {
        assertThat(slot("SHP1010")).isEqualTo("D5 12:00");
        assertThat(slot("SHP1016")).isEqualTo("D6 12:30");
        assertThat(slot("SHP1011")).isEqualTo("D6 14:00");
        // Heavy windows are 90 minutes; a 75-minute standard unload runs past a 60-minute slot.
        assertThat(a.get("SHP1016").endMin() - a.get("SHP1016").startMin()).isEqualTo(90);
        a.values().stream().filter(s -> s.getSlot() != null).forEach(s -> assertThat(s.endMin() - s.startMin())
                .as(s.getShipmentId()).isEqualTo(Math.max(s.getUnloadMin(), s.getSlot().lengthMin())));
    }

    @Test
    void reeferDockUnavailableSHP1015HasNoFeasibleSlotAndGoesToACoordinator() {
        assertThat(a.get("SHP1015").getSlot()).isNull();
        assertThat(load.emptyCandidateReasons()).containsEntry("SHP1015", "COMPATIBLE_SLOTS_BLOCKED");
    }

    @Test
    void simultaneousCompetitionSHP1006SHP1012SHP1013SHP1014GetsDistinctSlotsHighestPriorityFirst() {
        assertThat(load.flagged()).extracting(LoadResult.Note::shipmentId).contains("SHP1014");
        List<String> slots = new ArrayList<>();
        for (String id : List.of("SHP1006", "SHP1012", "SHP1013")) {
            slots.add(a.get(id).getSlot().getId());
        }
        assertThat(slots).doesNotHaveDuplicates();
        assertThat(a.get("SHP1006").startMin()).as("HIGH before the NORMAL trucks")
                .isLessThanOrEqualTo(Math.min(a.get("SHP1012").startMin(), a.get("SHP1013").startMin()));
    }

    @Test
    void priorityConflictSHP1009CriticalIsPlacedWithNoInversion() {
        assertThat(a.get("SHP1009").getSlot()).isNotNull();
        assertThat(clock(a.get("SHP1009").startMin())).isEqualTo("12:00");
        Kpis k = Kpis.of(plan, load.asOf().getEpochSecond() / 60, load.horizonEnd().getEpochSecond() / 60);
        assertThat(k.priorityInversions()).isZero();
    }

    @Test
    void appointmentHistoryAPT1012AAndAPT1016AStaysButIsNotPlannedOn() throws Exception {
        assertThat(one("SELECT count(*)::text FROM appointments WHERE appointment_id IN ('APT1012A', 'APT1016A')"
                + " AND is_current = 0")).isEqualTo("2");
        assertThat(a.get("SHP1012").getOriginalSlotId()).isNotEqualTo(one(
                "SELECT slot_id FROM appointments WHERE appointment_id = 'APT1012A'"));
    }

    @Test
    void pendingWarehouseConfirmationAPT1013AExpiredAndIsReplanned() {
        assertThat(a.get("SHP1013").getReleaseReason()).isEqualTo("HOLD_EXPIRED");
    }

    @Test
    void operatingHourLimitRULE005NoNewStartAfter2100() {
        a.values().stream().filter(s -> s.getSlot() != null)
                .forEach(s -> assertThat(clock(s.startMin())).as(s.getShipmentId()).isLessThanOrEqualTo("21:00"));
    }

    @Test
    void multiFacilityPlansJaipurAlone() throws Exception {
        assertThat(plan.getSlots()).allSatisfy(s -> assertThat(s.getId()).startsWith("SLOT-JAI-"));
        assertThat(one("SELECT string_agg(shipment_id, ',') FROM shipments WHERE destination_facility_id = 'FAC-GGN-01'"))
                .satisfies(ggn -> assertThat(a).doesNotContainKeys(ggn.split(",")));
    }

    static String one(String sql) throws Exception {
        try (Connection c = DriverManager.getConnection(SnapshotSolveIT.url);
                ResultSet rs = c.createStatement().executeQuery(sql)) {
            rs.next();
            return rs.getString(1);
        }
    }
}
