package com.setuhaul.solver.domain;

/** Penalty weights the constraints read from the plan (score level L4). */
public record PlanWeights(long unforcedMovePenalty, long forcedMovePenalty) {
}
