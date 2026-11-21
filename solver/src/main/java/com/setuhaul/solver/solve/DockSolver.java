package com.setuhaul.solver.solve;

import ai.timefold.solver.core.api.solver.Solver;
import ai.timefold.solver.core.api.solver.SolverFactory;
import ai.timefold.solver.core.config.solver.SolverConfig;
import ai.timefold.solver.core.config.solver.termination.TerminationConfig;

import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.constraints.DockConstraints;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.ShipmentAssignment;

import java.time.Duration;

/** Builds the Timefold solver for a {@link DockPlan} and runs it. */
public final class DockSolver {

    private DockSolver() {
    }

    /** Stop at the configured time limit, or earlier when the score stops improving. */
    public static TerminationConfig timeLimited(SolverSettings s) {
        return new TerminationConfig()
                .withSpentLimit(Duration.ofSeconds(s.solveSeconds()))
                .withUnimprovedSpentLimit(Duration.ofSeconds(s.unimprovedSeconds()));
    }

    /** Stop after a fixed number of steps: same input and seed give the same plan, on any machine. */
    public static TerminationConfig stepLimited(int steps) {
        return new TerminationConfig().withStepCountLimit(steps);
    }

    public static SolverConfig config(SolverSettings s, TerminationConfig termination) {
        return new SolverConfig()
                .withSolutionClass(DockPlan.class)
                .withEntityClasses(ShipmentAssignment.class)
                .withConstraintProviderClass(DockConstraints.class)
                .withRandomSeed(s.randomSeed())
                .withTerminationConfig(termination);
    }

    public static DockPlan solve(DockPlan problem, SolverSettings s, TerminationConfig termination) {
        SolverFactory<DockPlan> factory = SolverFactory.create(config(s, termination));
        Solver<DockPlan> solver = factory.buildSolver();
        return solver.solve(problem);
    }
}
