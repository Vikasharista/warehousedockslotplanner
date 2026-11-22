package com.setuhaul.solver.scale;

import ai.timefold.solver.core.api.solver.Solver;
import ai.timefold.solver.core.api.solver.SolverFactory;

import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.load.LoadResult;
import com.setuhaul.solver.load.PostgresLoader;
import com.setuhaul.solver.report.Kpis;
import com.setuhaul.solver.solve.DockSolver;

import java.sql.Connection;
import java.time.OffsetDateTime;
import java.util.Locale;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Plans one facility the way the service does (time limit plus unimproved limit) and records how long loading and
 * solving took, when the first feasible plan appeared and when the final best plan was found.
 */
public final class ScaleRun {

    /** One facility's run. Times in milliseconds from the start of solving; -1 when it never happened. */
    public record Result(String facilityId, int docks, int slots, int trucks, long loadMs, long solveMs,
            long firstFeasibleMs, long bestMs, boolean feasible, String score, Kpis kpis) {

        /** Passes when the plan is feasible and solving finished inside {@code limitSeconds}. */
        public boolean passes(long limitSeconds) {
            return feasible && solveMs <= limitSeconds * 1000;
        }

        public String toJson() {
            return String.format(Locale.ROOT,
                    "{\"facilityId\":\"%s\",\"docks\":%d,\"slots\":%d,\"trucks\":%d,\"loadMs\":%d,\"solveMs\":%d,"
                            + "\"firstFeasibleMs\":%d,\"bestMs\":%d,\"feasible\":%s,\"score\":\"%s\",\"assigned\":%d,"
                            + "\"unassigned\":%d,\"weightedWaitMin\":%.1f,\"priorityInversions\":%d,"
                            + "\"dockUtilisationPct\":%.1f}",
                    facilityId, docks, slots, trucks, loadMs, solveMs, firstFeasibleMs, bestMs, feasible, score,
                    kpis.assigned(), kpis.unassigned(), kpis.priorityWeightedWaitMin(), kpis.priorityInversions(),
                    kpis.dockUtilisationPct());
        }
    }

    private ScaleRun() {
    }

    public static Result run(Connection conn, String facilityId, OffsetDateTime asOf, SolverSettings settings)
            throws Exception {
        long t0 = System.nanoTime();
        LoadResult load = new PostgresLoader(settings).load(conn, facilityId, asOf);
        long loadMs = (System.nanoTime() - t0) / 1_000_000;

        Solver<DockPlan> solver = SolverFactory.<DockPlan>create(DockSolver.config(settings, DockSolver.timeLimited(settings)))
                .buildSolver();
        AtomicLong firstFeasible = new AtomicLong(-1);
        AtomicLong best = new AtomicLong(-1);
        long start = System.nanoTime();
        solver.addEventListener(e -> {
            long ms = (System.nanoTime() - start) / 1_000_000;
            best.set(ms);
            if (e.getNewBestScore().isFeasible()) {
                firstFeasible.compareAndSet(-1, ms);
            }
        });
        DockPlan solved = solver.solve(DockSolver.warmStart(load.plan(), settings));
        long solveMs = (System.nanoTime() - start) / 1_000_000;
        if (solved.getScore().isFeasible() && firstFeasible.get() < 0) {
            firstFeasible.set(0); // the starting plan was already feasible
        }
        Kpis k = Kpis.of(solved, load.asOf().getEpochSecond() / 60, load.horizonEnd().getEpochSecond() / 60);
        return new Result(facilityId, (int) solved.getSlots().stream().map(s -> s.getDock()).distinct().count(),
                solved.getSlots().size(), solved.getAssignments().size(), loadMs, solveMs, firstFeasible.get(),
                best.get(), solved.getScore().isFeasible(), solved.getScore().toString(), k);
    }
}
