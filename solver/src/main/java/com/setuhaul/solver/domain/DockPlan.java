package com.setuhaul.solver.domain;

import ai.timefold.solver.core.api.domain.solution.PlanningEntityCollectionProperty;
import ai.timefold.solver.core.api.domain.solution.PlanningScore;
import ai.timefold.solver.core.api.domain.solution.PlanningSolution;
import ai.timefold.solver.core.api.domain.solution.ProblemFactCollectionProperty;
import ai.timefold.solver.core.api.domain.solution.ProblemFactProperty;
import ai.timefold.solver.core.api.score.BendableScore;

import java.time.Instant;
import java.util.List;

/**
 * One facility's dock plan for the horizon starting at {@code asOf}.
 *
 * <p>Score levels, compared in order: hard 0 = H1-H12 violations; soft 0 = L1 unassigned trucks (priority weighted);
 * soft 1 = L2 priority inversions; soft 2 = L3 next-pickup inversions; soft 3 = L4 weighted waiting plus moves.
 */
@PlanningSolution
public class DockPlan {

    private String facilityId;
    private Instant asOf;

    @ProblemFactCollectionProperty
    private List<Dock> docks;
    @ProblemFactCollectionProperty
    private List<Slot> slots;
    @ProblemFactCollectionProperty
    private List<DockBlock> blocks;
    @ProblemFactProperty
    private PlanWeights weights;

    @PlanningEntityCollectionProperty
    private List<ShipmentAssignment> assignments;

    @PlanningScore(bendableHardLevelsSize = 1, bendableSoftLevelsSize = 4)
    private BendableScore score;

    public DockPlan() {
    }

    public DockPlan(String facilityId, Instant asOf, List<Dock> docks, List<Slot> slots, List<DockBlock> blocks,
            PlanWeights weights, List<ShipmentAssignment> assignments) {
        this.facilityId = facilityId;
        this.asOf = asOf;
        this.docks = docks;
        this.slots = slots;
        this.blocks = blocks;
        this.weights = weights;
        this.assignments = assignments;
    }

    public String getFacilityId() { return facilityId; }
    public Instant getAsOf() { return asOf; }
    public List<Dock> getDocks() { return docks; }
    public List<Slot> getSlots() { return slots; }
    public List<DockBlock> getBlocks() { return blocks; }
    public PlanWeights getWeights() { return weights; }
    public List<ShipmentAssignment> getAssignments() { return assignments; }
    public BendableScore getScore() { return score; }
    public void setScore(BendableScore score) { this.score = score; }
}
