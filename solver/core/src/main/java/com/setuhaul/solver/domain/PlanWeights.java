package com.setuhaul.solver.domain;

import java.util.Set;

/**
 * Penalty weights the constraints read from the plan: moves (score level L4) and bumping (L1).
 *
 * @param bumpPenalty cost of bumping one confirmed booking (L1)
 * @param bumpBy      priority codes allowed to take a bumped slot
 */
public record PlanWeights(long unforcedMovePenalty, long forcedMovePenalty, long bumpPenalty, Set<String> bumpBy) {

    public PlanWeights {
        bumpBy = Set.copyOf(bumpBy);
    }

    /** Weights without bumping. */
    public PlanWeights(long unforcedMovePenalty, long forcedMovePenalty) {
        this(unforcedMovePenalty, forcedMovePenalty, 0, Set.of());
    }
}
