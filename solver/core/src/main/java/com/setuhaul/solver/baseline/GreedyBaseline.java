package com.setuhaul.solver.baseline;

import com.setuhaul.solver.domain.DockBlock;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.ShipmentAssignment;
import com.setuhaul.solver.domain.Slot;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;

/**
 * The rules the solver is compared against (plan section "Success metrics", baseline B). Each one keeps the bookings
 * the loader kept (pinned, bumpable and held slots stay where they are), then places every other truck, one at a
 * time in the rule's order, on the earliest slot it can use: a candidate slot (dock type, weight, reefer, ready time,
 * last start, horizon) whose unload overlaps no dock block and no truck already placed on that dock. A truck with no
 * such slot stays unassigned. The rules never bump a booking.
 */
public final class GreedyBaseline {

    /** The order trucks pick their slot in. */
    public enum Rule {
        /** Baseline B: first come, first served, by earliest ready time (effective ETA + buffer, or gate-in). */
        FCFS(Comparator.comparingLong(ShipmentAssignment::getEarliestReadyMin)
                .thenComparing(ShipmentAssignment::getShipmentId)),
        /** A careful planner by hand: highest priority first, then by earliest ready time. */
        PRIORITY_FIRST(Comparator.comparingInt(ShipmentAssignment::getPriorityWeight).reversed()
                .thenComparingLong(ShipmentAssignment::getEarliestReadyMin)
                .thenComparing(ShipmentAssignment::getShipmentId));

        private final Comparator<ShipmentAssignment> order;

        Rule(Comparator<ShipmentAssignment> order) {
            this.order = order;
        }
    }

    private GreedyBaseline() {
    }

    /** Assigns the open trucks of {@code plan} in place by {@code rule} and returns the same plan. */
    public static DockPlan apply(DockPlan plan, Rule rule) {
        List<ShipmentAssignment> placed = new ArrayList<>();
        List<ShipmentAssignment> open = new ArrayList<>();
        for (ShipmentAssignment a : plan.getAssignments()) {
            if (a.getSlot() != null) {
                placed.add(a);
            } else {
                open.add(a);
            }
        }
        open.sort(rule.order);
        for (ShipmentAssignment a : open) {
            a.getCandidateSlots().stream()
                    .sorted(Comparator.comparingLong(Slot::getStartMin).thenComparing(Slot::getId))
                    .filter(s -> fits(a, s, placed, plan.getBlocks()))
                    .findFirst()
                    .ifPresent(s -> {
                        a.setSlot(s);
                        placed.add(a);
                    });
        }
        return plan;
    }

    static boolean fits(ShipmentAssignment a, Slot s, List<ShipmentAssignment> placed, List<DockBlock> blocks) {
        long start = s.getStartMin();
        long end = start + Math.max(a.getUnloadMin(), s.lengthMin());
        for (DockBlock b : blocks) {
            if (b.dock().equals(s.getDock()) && b.overlaps(start, end)) {
                return false;
            }
        }
        for (ShipmentAssignment p : placed) {
            if (p.dock().equals(s.getDock()) && p.startMin() < end && start < p.endMin()) {
                return false;
            }
        }
        return true;
    }
}
