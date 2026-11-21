package com.setuhaul.solver.load;

import com.setuhaul.solver.domain.DockPlan;

import java.time.Instant;
import java.time.ZoneId;
import java.util.List;
import java.util.Map;

/**
 * Everything the loader read for one facility at one as-of time: the plan to solve, plus the trucks that are not
 * in the plan and why.
 *
 * @param emptyCandidateReasons trucks that entered the plan with no usable slot, mapped to the reason
 */
public record LoadResult(
        DockPlan plan,
        ZoneId zone,
        Instant asOf,
        Instant horizonEnd,
        List<Note> flagged,
        List<Note> inDock,
        List<Note> outsideHorizon,
        Map<String, String> emptyCandidateReasons) {

    /** A truck left out of the solve, with the reason and a human-readable detail. */
    public record Note(String shipmentId, String reason, String detail) {
    }
}
