package com.setuhaul.solver.report;

import com.setuhaul.solver.domain.DockBlock;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.ShipmentAssignment;
import com.setuhaul.solver.domain.Slot;

import java.util.List;

/**
 * The plan's success metrics (plan section "Objective and success metrics"), computed from a solved plan.
 * Dock utilisation is occupied minutes divided by OPEN slot minutes on active docks, both within the horizon.
 */
public record Kpis(
        int trucksPlanned,
        int assigned,
        int unassigned,
        double averageWaitMin,
        double priorityWeightedWaitMin,
        int priorityInversions,
        int pickupInversions,
        int forcedMoves,
        int unforcedMoves,
        double dockUtilisationPct) {

    public static Kpis of(DockPlan plan, long horizonStartMin, long horizonEndMin) {
        List<ShipmentAssignment> all = plan.getAssignments();
        List<ShipmentAssignment> assigned = all.stream().filter(a -> a.getSlot() != null).toList();

        double avgWait = assigned.stream().mapToLong(ShipmentAssignment::waitMin).average().orElse(0);
        long weightSum = assigned.stream().mapToLong(ShipmentAssignment::getPriorityWeight).sum();
        double weightedWait = weightSum == 0 ? 0
                : (double) assigned.stream().mapToLong(a -> a.waitMin() * a.getPriorityWeight()).sum() / weightSum;

        int inversions = 0;
        int pickupInversions = 0;
        for (ShipmentAssignment hi : assigned) {
            for (ShipmentAssignment lo : assigned) {
                if (lo == hi || lo.isPinned() || lo.startMin() >= hi.startMin() || !hi.canUse(lo.getSlot())) {
                    continue;
                }
                if (hi.getPriorityWeight() > lo.getPriorityWeight()) {
                    inversions++;
                } else if (hi.getPriorityWeight() == lo.getPriorityWeight() && hi.pickupMoreUrgentThan(lo)) {
                    pickupInversions++;
                }
            }
        }
        int forced = (int) assigned.stream().filter(a -> a.wasForcedOffBooking() && a.movedOffOriginal()).count();
        int unforced = (int) assigned.stream()
                .filter(a -> a.isConfirmedBooking() && !a.wasForcedOffBooking() && a.movedOffOriginal()).count();

        long openMinutes = 0;
        for (Slot s : plan.getSlots()) {
            if (s.getDock().isActive() && plan.getBlocks().stream().noneMatch(b -> b.kind() == DockBlock.Kind.BLOCKED_SLOT
                    && b.reference().equals(s.getId()))) {
                openMinutes += clip(s.getStartMin(), s.getEndMin(), horizonStartMin, horizonEndMin);
            }
        }
        long occupied = assigned.stream().mapToLong(a -> clip(a.startMin(), a.endMin(), horizonStartMin, horizonEndMin)).sum()
                + plan.getBlocks().stream()
                        .filter(b -> b.kind() == DockBlock.Kind.TRUCK_IN_DOCK || b.kind() == DockBlock.Kind.FLAGGED_BOOKING)
                        .mapToLong(b -> clip(b.startMin(), b.endMin(), horizonStartMin, horizonEndMin)).sum();
        double utilisation = openMinutes == 0 ? 0 : 100.0 * occupied / openMinutes;

        return new Kpis(all.size(), assigned.size(), all.size() - assigned.size(), avgWait, weightedWait,
                inversions, pickupInversions, forced, unforced, utilisation);
    }

    private static long clip(long start, long end, long from, long to) {
        return Math.max(0, Math.min(end, to) - Math.max(start, from));
    }
}
