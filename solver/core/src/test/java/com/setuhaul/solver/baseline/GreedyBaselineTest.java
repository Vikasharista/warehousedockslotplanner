package com.setuhaul.solver.baseline;

import com.setuhaul.solver.baseline.GreedyBaseline.Rule;
import com.setuhaul.solver.domain.Compatibility;
import com.setuhaul.solver.domain.Dock;
import com.setuhaul.solver.domain.DockBlock;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.PlanWeights;
import com.setuhaul.solver.domain.ShipmentAssignment;
import com.setuhaul.solver.domain.Slot;

import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;

class GreedyBaselineTest {

    // Minutes; 600 = 10:00.
    static final Dock D1 = new Dock("D1", "D1", Dock.Type.STANDARD, false, 20_000, true);
    static final Dock D2 = new Dock("D2", "D2", Dock.Type.STANDARD, false, 25_000, true);
    static final Slot D1_10 = slot("D1-10", D1, 600);
    static final Slot D1_11 = slot("D1-11", D1, 660);
    static final Slot D1_12 = slot("D1-12", D1, 720);
    static final Slot D2_11 = slot("D2-11", D2, 660);
    static final List<Slot> SLOTS = List.of(D1_10, D1_11, D1_12, D2_11);

    static Slot slot(String id, Dock dock, long start) {
        return new Slot(id, dock, start, start + 60, true, false);
    }

    static ShipmentAssignment truck(String id, int weight, long ready, int unload) {
        ShipmentAssignment a = new ShipmentAssignment(id, "P" + weight, weight, null, false, 10_000, unload, ready);
        a.setCandidateSlots(SLOTS.stream().filter(s -> Compatibility.violation(a, s) == null).toList());
        return a;
    }

    static DockPlan plan(List<DockBlock> blocks, ShipmentAssignment... trucks) {
        return new DockPlan("FAC", Instant.EPOCH, List.of(D1, D2), SLOTS, blocks, new PlanWeights(240, 30),
                new ArrayList<>(List.of(trucks)));
    }

    @Test
    void firstComeFirstServedGoesByReadyTimeWhateverThePriority() {
        var low = truck("LOW", 1, 590, 60);
        var critical = truck("CRIT", 27, 600, 60);
        GreedyBaseline.apply(plan(List.of(), low, critical), Rule.FCFS);
        assertThat(low.getSlot()).isEqualTo(D1_10);
        assertThat(critical.getSlot()).as("next earliest free slot, D1 and D2 at 11:00 tie on id").isEqualTo(D1_11);
    }

    @Test
    void priorityFirstServesTheHighestPriorityFirst() {
        var low = truck("LOW", 1, 590, 60);
        var critical = truck("CRIT", 27, 600, 60);
        GreedyBaseline.apply(plan(List.of(), low, critical), Rule.PRIORITY_FIRST);
        assertThat(critical.getSlot()).isEqualTo(D1_10);
        assertThat(low.getSlot()).isEqualTo(D1_11);
    }

    @Test
    void aSlotUnderADockBlockOrALongUnloadIsSkipped() {
        var blocked = new DockBlock(D1, 600, 660, DockBlock.Kind.DOCK_EVENT, "EVT");
        var a = truck("A", 3, 600, 75); // D1 10:00 is blocked; D1 11:00 runs to 12:15
        var b = truck("B", 3, 700, 60); // ready 11:40: only D1 12:00 is late enough
        GreedyBaseline.apply(plan(List.of(blocked), a, b), Rule.FCFS);
        assertThat(a.getSlot()).isEqualTo(D1_11);
        assertThat(b.getSlot()).as("D1 12:00 overlaps A's unload").isNull();
    }

    @Test
    void keptBookingsStayAndATruckWithNoFreeSlotStaysUnassigned() {
        var kept = truck("KEPT", 1, 600, 60);
        kept.setSlot(D1_10);
        kept.setPinned(true);
        var late = truck("LATE", 27, 700, 60); // ready 11:40: only D1 12:00 left
        var later = truck("LATER", 27, 710, 60);
        GreedyBaseline.apply(plan(List.of(), kept, late, later), Rule.PRIORITY_FIRST);
        assertThat(kept.getSlot()).isEqualTo(D1_10);
        assertThat(late.getSlot()).isEqualTo(D1_12);
        assertThat(later.getSlot()).isNull();
    }
}
