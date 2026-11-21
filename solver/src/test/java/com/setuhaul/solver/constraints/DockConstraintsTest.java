package com.setuhaul.solver.constraints;

import ai.timefold.solver.core.api.score.stream.test.ConstraintVerifier;

import com.setuhaul.solver.domain.Compatibility;
import com.setuhaul.solver.domain.Dock;
import com.setuhaul.solver.domain.DockBlock;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.PlanWeights;
import com.setuhaul.solver.domain.ShipmentAssignment;
import com.setuhaul.solver.domain.ShipmentAssignment.BookingStatus;
import com.setuhaul.solver.domain.Slot;

import org.junit.jupiter.api.Nested;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;

class DockConstraintsTest {

    // Times in minutes; 600 = 10:00 on an arbitrary day.
    static final Dock D1 = new Dock("D1", "D1", Dock.Type.STANDARD, false, 20_000, true);
    static final Dock D2 = new Dock("D2", "D2", Dock.Type.STANDARD, false, 25_000, true);
    static final Dock REEFER = new Dock("D5", "D5", Dock.Type.REEFER, true, 22_000, true);
    static final Dock HEAVY = new Dock("D6", "D6", Dock.Type.HEAVY, false, 35_000, true);

    static final Slot D1_10 = slot("D1-10", D1, 600);
    static final Slot D1_11 = slot("D1-11", D1, 660);
    static final Slot D2_10 = slot("D2-10", D2, 600);
    static final Slot D2_11 = slot("D2-11", D2, 660);
    static final Slot REEFER_10 = slot("D5-10", REEFER, 600);
    static final Slot HEAVY_10 = new Slot("D6-10", HEAVY, 600, 690, true, false);
    static final Slot D1_BLOCKED = new Slot("D1-12", D1, 720, 780, false, false);
    static final Slot D1_LATE = new Slot("D1-21", D1, 1290, 1350, true, true);

    static final List<Slot> ALL = List.of(D1_10, D1_11, D2_10, D2_11, REEFER_10, HEAVY_10, D1_BLOCKED, D1_LATE);

    final ConstraintVerifier<DockConstraints, DockPlan> verifier =
            ConstraintVerifier.build(new DockConstraints(), DockPlan.class, ShipmentAssignment.class);

    static Slot slot(String id, Dock dock, long start) {
        return new Slot(id, dock, start, start + 60, true, false);
    }

    /** A standard-dock truck of the given priority weight, ready at {@code ready}, unloading in {@code unload}. */
    static ShipmentAssignment truck(String id, int weight, long ready, int unload) {
        return truck(id, weight, null, false, 10_000, unload, ready);
    }

    static ShipmentAssignment truck(String id, int weight, Dock.Type needs, boolean reefer, int kg, int unload, long ready) {
        ShipmentAssignment a = new ShipmentAssignment(id, "P" + weight, weight, needs, reefer, kg, unload, ready);
        a.setCandidateSlots(ALL.stream().filter(s -> Compatibility.violation(a, s) == null).toList());
        return a;
    }

    static ShipmentAssignment at(ShipmentAssignment a, Slot s) {
        a.setSlot(s);
        return a;
    }

    @Nested
    class Hard {

        @Test
        void overlappingTrucksOnOneDockArePenalisedByOverlapMinutes() {
            var a = at(truck("A", 3, 600, 60), D1_10);
            var b = at(truck("B", 3, 600, 60), D1_10);
            verifier.verifyThat(DockConstraints::dockOverlap).given(a, b).penalizesBy(60);
        }

        @Test
        void aLongUnloadRunsIntoTheNextSlot() {
            var longUnload = at(truck("A", 3, 600, 75), D1_10); // occupies 600-675
            var next = at(truck("B", 3, 600, 60), D1_11);       // 660-720
            verifier.verifyThat(DockConstraints::dockOverlap).given(longUnload, next).penalizesBy(15);
        }

        @Test
        void backToBackOrDifferentDocksDoNotOverlap() {
            var a = at(truck("A", 3, 600, 60), D1_10);
            var b = at(truck("B", 3, 600, 60), D1_11);
            var c = at(truck("C", 3, 600, 60), D2_10);
            verifier.verifyThat(DockConstraints::dockOverlap).given(a, b, c).hasNoImpact();
        }

        @Test
        void unloadRunningIntoADockBlockIsPenalised() {
            var block = new DockBlock(D1, 640, 700, DockBlock.Kind.DOCK_EVENT, "DEVT");
            var a = at(truck("A", 3, 600, 60), D1_10);
            verifier.verifyThat(DockConstraints::blockedDock).given(a, block).penalizesBy(20);
        }

        @Test
        void aBlockOnAnotherDockHasNoImpact() {
            var block = new DockBlock(D2, 600, 700, DockBlock.Kind.TRUCK_IN_DOCK, "SHP");
            var a = at(truck("A", 3, 600, 60), D1_10);
            verifier.verifyThat(DockConstraints::blockedDock).given(a, block).hasNoImpact();
        }

        @Test
        void h1SlotBeforeEarliestReady() {
            var late = at(truck("A", 3, 630, 60), D1_10);
            assertThat(Compatibility.violation(late, D1_10)).isEqualTo(Compatibility.Violation.H1_BEFORE_EARLIEST_READY);
            verifier.verifyThat(DockConstraints::incompatibleSlot).given(late).penalizesBy(1);
        }

        @Test
        void h3ReeferLoadOnStandardDock() {
            var reefer = at(truck("A", 3, null, true, 15_000, 60, 600), D1_10);
            assertThat(Compatibility.violation(reefer, D1_10)).isEqualTo(Compatibility.Violation.H3_REEFER_DOCK_REQUIRED);
            verifier.verifyThat(DockConstraints::incompatibleSlot).given(reefer).penalizesBy(1);
            assertThat(reefer.getCandidateSlots()).containsExactly(REEFER_10);
        }

        @Test
        void h4HeavyLoadNeedsTheHeavyDock() {
            var heavy = at(truck("A", 3, Dock.Type.HEAVY, false, 30_000, 90, 600), D2_10);
            assertThat(Compatibility.violation(heavy, D2_10)).isIn(
                    Compatibility.Violation.H4_DOCK_TYPE_MISMATCH, Compatibility.Violation.H10_OVER_DOCK_WEIGHT);
            verifier.verifyThat(DockConstraints::incompatibleSlot).given(heavy).penalizesBy(1);
            assertThat(heavy.getCandidateSlots()).containsExactly(HEAVY_10);
        }

        @Test
        void h5UnavailableSlot() {
            var a = at(truck("A", 3, 600, 60), D1_BLOCKED);
            assertThat(Compatibility.violation(a, D1_BLOCKED)).isEqualTo(Compatibility.Violation.H5_SLOT_UNAVAILABLE);
            verifier.verifyThat(DockConstraints::incompatibleSlot).given(a).penalizesBy(1);
        }

        @Test
        void h10LoadHeavierThanTheDockAllows() {
            var heavy = at(truck("A", 3, null, false, 20_500, 75, 600), D1_10); // like SHP1005 on a 20,000 kg dock
            assertThat(Compatibility.violation(heavy, D1_10)).isEqualTo(Compatibility.Violation.H10_OVER_DOCK_WEIGHT);
            verifier.verifyThat(DockConstraints::incompatibleSlot).given(heavy).penalizesBy(1);
            assertThat(heavy.getCandidateSlots()).doesNotContain(D1_10, D1_11).contains(D2_10, D2_11);
        }

        @Test
        void h11SlotAfterTheLastAllowedStart() {
            var a = at(truck("A", 3, 600, 60), D1_LATE);
            assertThat(Compatibility.violation(a, D1_LATE)).isEqualTo(Compatibility.Violation.H11_AFTER_LAST_START);
            verifier.verifyThat(DockConstraints::incompatibleSlot).given(a).penalizesBy(1);
        }

        @Test
        void aCompatibleSlotHasNoImpact() {
            var a = at(truck("A", 3, 600, 60), D1_10);
            verifier.verifyThat(DockConstraints::incompatibleSlot).given(a).hasNoImpact();
        }
    }

    @Nested
    class Soft {

        @Test
        void l1UnassignedTruckCostsItsPriorityWeight() {
            var critical = truck("A", 27, 600, 60);
            var normal = at(truck("B", 3, 600, 60), D1_10);
            verifier.verifyThat(DockConstraints::unassigned).given(critical, normal).penalizesBy(27);
        }

        @Test
        void l2LowerPriorityTakingAnEarlierUsableSlotIsAnInversion() {
            var high = at(truck("HI", 9, 600, 60), D1_11);
            var low = at(truck("LO", 3, 600, 60), D2_10);
            verifier.verifyThat(DockConstraints::priorityInversion).given(high, low).penalizesBy(6);
        }

        @Test
        void l2NoInversionWhenTheHighPriorityTruckCouldNotUseTheSlot() {
            var high = at(truck("HI", 9, 650, 60), D1_11);   // not ready before 10:50, so 10:00 is not usable
            var low = at(truck("LO", 3, 600, 60), D2_10);
            verifier.verifyThat(DockConstraints::priorityInversion).given(high, low).hasNoImpact();
        }

        @Test
        void l2PinnedBookingsAreCommitmentsNotInversions() {
            var high = at(truck("HI", 9, 600, 60), D1_11);
            var low = at(truck("LO", 3, 600, 60), D2_10);
            low.setPinned(true);
            verifier.verifyThat(DockConstraints::priorityInversion).given(high, low).hasNoImpact();
        }

        @Test
        void l3EqualPriorityTruckWithTheNearerPickupShouldGoFirst() {
            var withPickup = at(truck("A", 3, 600, 60), D1_11);
            withPickup.setNextPickupMin(900L);
            var without = at(truck("B", 3, 600, 60), D2_10);
            verifier.verifyThat(DockConstraints::pickupInversion).given(withPickup, without).penalizesBy(1);
        }

        @Test
        void l3NoPickupOnEitherSideHasNoImpact() {
            var a = at(truck("A", 3, 600, 60), D1_11);
            var b = at(truck("B", 3, 600, 60), D2_10);
            verifier.verifyThat(DockConstraints::pickupInversion).given(a, b).hasNoImpact();
        }

        @Test
        void l4WaitingMinutesAreWeightedByPriority() {
            var a = at(truck("A", 3, 600, 60), D1_11); // ready 10:00, starts 11:00
            verifier.verifyThat(DockConstraints::weightedWait).given(a).penalizesBy(180);
        }

        @Test
        void l4MovingAFeasibleConfirmedBookingCostsTheUnforcedPenalty() {
            var a = at(truck("A", 3, 600, 60), D1_11);
            a.setOriginalBooking(D1_10, BookingStatus.CONFIRMED, null);
            verifier.verifyThat(DockConstraints::unforcedMove).given(a, new PlanWeights(240, 30)).penalizesBy(240);
            verifier.verifyThat(DockConstraints::forcedMove).given(a, new PlanWeights(240, 30)).hasNoImpact();
        }

        @Test
        void l4ReplacingABookingThatCouldNotBeKeptCostsTheForcedPenalty() {
            var a = at(truck("A", 3, 600, 60), D1_11);
            a.setOriginalBooking(D1_BLOCKED, BookingStatus.CONFIRMED, "SLOT_UNAVAILABLE");
            verifier.verifyThat(DockConstraints::forcedMove).given(a, new PlanWeights(240, 30)).penalizesBy(30);
            verifier.verifyThat(DockConstraints::unforcedMove).given(a, new PlanWeights(240, 30)).hasNoImpact();
        }

        @Test
        void l4KeepingTheBookingOrReplacingADriverHoldCostsNothing() {
            var kept = at(truck("A", 3, 600, 60), D1_10);
            kept.setOriginalBooking(D1_10, BookingStatus.CONFIRMED, null);
            var hold = at(truck("B", 3, 600, 60), D2_11);
            hold.setOriginalBooking(D2_10, BookingStatus.PENDING_CONFIRMATION, "HOLD_EXPIRED");
            var w = new PlanWeights(240, 30);
            verifier.verifyThat(DockConstraints::unforcedMove).given(kept, hold, w).hasNoImpact();
            verifier.verifyThat(DockConstraints::forcedMove).given(kept, hold, w).hasNoImpact();
        }
    }
}
