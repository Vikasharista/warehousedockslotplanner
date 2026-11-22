package com.setuhaul.solver.constraints;

import ai.timefold.solver.core.api.score.BendableScore;
import ai.timefold.solver.core.api.score.stream.ConstraintCollectors;
import ai.timefold.solver.core.api.score.stream.Constraint;
import ai.timefold.solver.core.api.score.stream.ConstraintFactory;
import ai.timefold.solver.core.api.score.stream.ConstraintProvider;
import ai.timefold.solver.core.api.score.stream.Joiners;

import com.setuhaul.solver.domain.Compatibility;
import com.setuhaul.solver.domain.DockBlock;
import com.setuhaul.solver.domain.PlanWeights;
import com.setuhaul.solver.domain.ShipmentAssignment;

/**
 * The plan's constraints. Hard level 0 holds H1-H12; soft levels 0-3 hold L1-L4 (see {@code DockPlan}).
 * Most per-slot rules are also enforced by each truck's candidate list; {@link #incompatibleSlot} re-checks them so
 * that a pinned or hand-edited assignment can never break them silently.
 */
public class DockConstraints implements ConstraintProvider {

    public static final String DOCK_OVERLAP = "H7 one truck per dock at a time";
    public static final String BLOCKED_DOCK = "H6 H11 H12 dock blocked closed or occupied";
    public static final String INCOMPATIBLE_SLOT = "H1 H3 H4 H5 H10 H11 incompatible slot";
    public static final String UNASSIGNED = "L1 unassigned truck";
    public static final String PRIORITY_INVERSION = "L2 priority inversion";
    public static final String PICKUP_INVERSION = "L3 next-pickup inversion";
    public static final String WEIGHTED_WAIT = "L4 priority-weighted waiting";
    public static final String UNFORCED_MOVE = "L4 unforced move of a confirmed booking";
    public static final String FORCED_MOVE = "L4 forced move of a confirmed booking";
    public static final String BUMP = "L1 confirmed booking bumped";
    public static final String BUMPED_SLOT_TAKER = "H13 only a bumping priority may take a bumped slot";
    public static final String ONE_BUMP_EACH = "H14 at most one bumped booking per truck";

    private static final BendableScore HARD = BendableScore.ofHard(1, 4, 0, 1);
    private static final BendableScore L1 = BendableScore.ofSoft(1, 4, 0, 1);
    private static final BendableScore L2 = BendableScore.ofSoft(1, 4, 1, 1);
    private static final BendableScore L3 = BendableScore.ofSoft(1, 4, 2, 1);
    private static final BendableScore L4 = BendableScore.ofSoft(1, 4, 3, 1);

    @Override
    public Constraint[] defineConstraints(ConstraintFactory f) {
        return new Constraint[] {
            dockOverlap(f),
            blockedDock(f),
            incompatibleSlot(f),
            unassigned(f),
            priorityInversion(f),
            pickupInversion(f),
            weightedWait(f),
            unforcedMove(f),
            forcedMove(f),
            bump(f),
            bumpedSlotTaker(f),
            oneBumpEach(f),
        };
    }

    /** H7: two trucks on one dock may not overlap; penalised by the minutes of overlap. */
    Constraint dockOverlap(ConstraintFactory f) {
        return f.forEachUniquePair(ShipmentAssignment.class,
                        Joiners.equal(ShipmentAssignment::dock),
                        Joiners.overlapping(ShipmentAssignment::startMin, ShipmentAssignment::endMin))
                .penalize(HARD, (a, b) -> overlap(a.startMin(), a.endMin(), b.startMin(), b.endMin()))
                .asConstraint(DOCK_OVERLAP);
    }

    /** H6/H11/H12: a truck's dock time may not run into a dock event, a blocked slot, closing time or a fixed truck. */
    Constraint blockedDock(ConstraintFactory f) {
        return f.forEach(ShipmentAssignment.class)
                .join(DockBlock.class,
                        Joiners.equal(ShipmentAssignment::dock, DockBlock::getDock),
                        Joiners.overlapping(ShipmentAssignment::startMin, ShipmentAssignment::endMin,
                                DockBlock::getStartMin, DockBlock::getEndMin))
                .penalize(HARD, (a, b) -> overlap(a.startMin(), a.endMin(), b.getStartMin(), b.getEndMin()))
                .asConstraint(BLOCKED_DOCK);
    }

    /** H1, H3, H4, H5, H10, H11 on the assigned slot. */
    Constraint incompatibleSlot(ConstraintFactory f) {
        return f.forEach(ShipmentAssignment.class)
                .filter(a -> Compatibility.violation(a, a.getSlot()) != null)
                .penalize(HARD)
                .asConstraint(INCOMPATIBLE_SLOT);
    }

    /** L1: a truck left without a slot goes to a coordinator; weighted by priority. */
    Constraint unassigned(ConstraintFactory f) {
        return f.forEachIncludingUnassigned(ShipmentAssignment.class)
                .filter(a -> a.getSlot() == null)
                .penalize(L1, ShipmentAssignment::getPriorityWeight)
                .asConstraint(UNASSIGNED);
    }

    /**
     * L2: a lower-priority truck starts earlier in a slot the higher-priority truck could have used.
     * Fixed (pinned) bookings are commitments and never count. Penalised by the gap in priority weight.
     */
    Constraint priorityInversion(ConstraintFactory f) {
        return f.forEach(ShipmentAssignment.class)
                .join(ShipmentAssignment.class,
                        Joiners.greaterThan(ShipmentAssignment::getPriorityWeight),
                        Joiners.filtering((hi, lo) -> !lo.isCommitted()
                                && lo.startMin() < hi.startMin()
                                && hi.canUse(lo.getSlot())))
                .penalize(L2, (hi, lo) -> hi.getPriorityWeight() - lo.getPriorityWeight())
                .asConstraint(PRIORITY_INVERSION);
    }

    /** L3: between trucks of equal priority, the one with the nearer next pickup should not wait behind the other. */
    Constraint pickupInversion(ConstraintFactory f) {
        return f.forEach(ShipmentAssignment.class)
                .join(ShipmentAssignment.class,
                        Joiners.equal(ShipmentAssignment::getPriorityWeight),
                        Joiners.filtering((urgent, other) -> !other.isCommitted()
                                && urgent.pickupMoreUrgentThan(other)
                                && other.startMin() < urgent.startMin()
                                && urgent.canUse(other.getSlot())))
                .penalize(L3)
                .asConstraint(PICKUP_INVERSION);
    }

    /** L4: minutes from earliest ready to slot start, times the priority weight. */
    Constraint weightedWait(ConstraintFactory f) {
        return f.forEach(ShipmentAssignment.class)
                .filter(a -> a.waitMin() > 0)
                .penalize(L4, a -> a.waitMin() * a.getPriorityWeight())
                .asConstraint(WEIGHTED_WAIT);
    }

    /** L4: a CONFIRMED booking that was still feasible was moved anyway. */
    Constraint unforcedMove(ConstraintFactory f) {
        return f.forEach(ShipmentAssignment.class)
                .filter(a -> a.isConfirmedBooking() && !a.wasForcedOffBooking() && a.movedOffOriginal())
                .join(PlanWeights.class)
                .penalize(L4, (a, w) -> w.unforcedMovePenalty())
                .asConstraint(UNFORCED_MOVE);
    }

    /** L4: a CONFIRMED booking that could not be kept was replaced by another slot. */
    Constraint forcedMove(ConstraintFactory f) {
        return f.forEach(ShipmentAssignment.class)
                .filter(a -> a.wasForcedOffBooking() && a.movedOffOriginal())
                .join(PlanWeights.class)
                .penalize(L4, (a, w) -> w.forcedMovePenalty())
                .asConstraint(FORCED_MOVE);
    }

    /**
     * L1: a feasible CONFIRMED booking left its slot (moved, or unassigned) to make room for a CRITICAL truck. The
     * penalty sits between the weights of the priorities that may bump and the rest, so only placing a bumping truck
     * pays for it (checked when the settings load).
     */
    Constraint bump(ConstraintFactory f) {
        return f.forEachIncludingUnassigned(ShipmentAssignment.class)
                .filter(ShipmentAssignment::isBumped)
                .join(PlanWeights.class)
                .penalize(L1, (a, w) -> w.bumpPenalty())
                .asConstraint(BUMP);
    }

    /** H13: only a truck of a bumping priority (CRITICAL) may use the slot a bumped booking left. */
    Constraint bumpedSlotTaker(ConstraintFactory f) {
        return f.forEach(ShipmentAssignment.class)
                .join(f.forEachIncludingUnassigned(ShipmentAssignment.class),
                        Joiners.filtering((taker, bumped) -> taker != bumped && bumped.isBumped()
                                && taker.takesSlotOf(bumped)))
                .join(PlanWeights.class,
                        Joiners.filtering((taker, bumped, w) -> !w.bumpBy().contains(taker.getPriorityCode())))
                .penalize(HARD)
                .asConstraint(BUMPED_SLOT_TAKER);
    }

    /** H14: one truck may take the slot of at most one bumped booking. */
    Constraint oneBumpEach(ConstraintFactory f) {
        return f.forEach(ShipmentAssignment.class)
                .join(f.forEachIncludingUnassigned(ShipmentAssignment.class),
                        Joiners.filtering((taker, bumped) -> taker != bumped && bumped.isBumped()
                                && taker.takesSlotOf(bumped)))
                .groupBy((taker, bumped) -> taker, ConstraintCollectors.countBi())
                .filter((taker, n) -> n > 1)
                .penalize(HARD, (taker, n) -> n - 1)
                .asConstraint(ONE_BUMP_EACH);
    }

    static long overlap(long aStart, long aEnd, long bStart, long bEnd) {
        return Math.max(0, Math.min(aEnd, bEnd) - Math.max(aStart, bStart));
    }
}
