package com.setuhaul.solver.baseline;

import ai.timefold.solver.core.api.score.BendableScore;
import ai.timefold.solver.core.api.solver.SolutionManager;
import ai.timefold.solver.core.api.solver.SolverFactory;
import ai.timefold.solver.core.config.solver.termination.TerminationConfig;

import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.load.LoadResult;
import com.setuhaul.solver.load.PostgresLoader;
import com.setuhaul.solver.report.Kpis;
import com.setuhaul.solver.solve.DockSolver;

import java.sql.Connection;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

/**
 * Plans one facility at one as-of time with the solver and with each {@link GreedyBaseline.Rule}, on the same loaded
 * data, and scores every plan with the same constraints and KPIs. Writes nothing.
 */
public final class Comparison {

    /** One method's plan for one facility. */
    public record Result(String facilityId, String method, boolean feasible, String score, long planMs, Kpis kpis) {

        /** One JSON object (no library needed for these flat values). */
        public String toJson() {
            Kpis k = kpis;
            return String.format(Locale.ROOT,
                    "{\"facilityId\":\"%s\",\"method\":\"%s\",\"feasible\":%s,\"score\":\"%s\",\"planMs\":%d,"
                            + "\"trucks\":%d,\"assigned\":%d,\"unassigned\":%d,\"avgWaitMin\":%.1f,"
                            + "\"weightedWaitMin\":%.1f,\"priorityInversions\":%d,\"pickupInversions\":%d,"
                            + "\"forcedMoves\":%d,\"unforcedMoves\":%d,\"dockUtilisationPct\":%.1f}",
                    facilityId, method, feasible, score, planMs, k.trucksPlanned(), k.assigned(), k.unassigned(),
                    k.averageWaitMin(), k.priorityWeightedWaitMin(), k.priorityInversions(), k.pickupInversions(),
                    k.forcedMoves(), k.unforcedMoves(), k.dockUtilisationPct());
        }
    }

    public static final String SOLVER = "SOLVER";
    public static final String SOLVER_COLD = "SOLVER_COLD_START";

    private Comparison() {
    }

    public static List<Result> run(Connection conn, String facilityId, OffsetDateTime asOf, SolverSettings settings,
            TerminationConfig termination) throws Exception {
        PostgresLoader loader = new PostgresLoader(settings);
        SolutionManager<DockPlan, BendableScore> scorer = SolutionManager.create(
                SolverFactory.create(DockSolver.config(settings, termination)));
        List<Result> out = new ArrayList<>();

        LoadResult load = loader.load(conn, facilityId, asOf);
        long t0 = System.nanoTime();
        DockPlan solved = DockSolver.solve(load.plan(), settings, termination);
        out.add(result(facilityId, SOLVER, solved, scorer, load, (System.nanoTime() - t0) / 1_000_000));

        // The same search started from an empty plan (warm_start=NONE), as before phase 6, for comparison.
        if (!"NONE".equals(settings.warmStart())) {
            SolverSettings cold = settings.withWarmStart("NONE");
            LoadResult coldLoad = loader.load(conn, facilityId, asOf);
            long tc = System.nanoTime();
            DockPlan coldSolved = DockSolver.solve(coldLoad.plan(), cold, termination);
            out.add(result(facilityId, SOLVER_COLD, coldSolved, scorer, coldLoad, (System.nanoTime() - tc) / 1_000_000));
        }

        for (GreedyBaseline.Rule rule : GreedyBaseline.Rule.values()) {
            LoadResult fresh = loader.load(conn, facilityId, asOf);
            long t1 = System.nanoTime();
            DockPlan plan = GreedyBaseline.apply(fresh.plan(), rule);
            out.add(result(facilityId, rule.name(), plan, scorer, fresh, (System.nanoTime() - t1) / 1_000_000));
        }
        return out;
    }

    private static Result result(String facilityId, String method, DockPlan plan,
            SolutionManager<DockPlan, BendableScore> scorer, LoadResult load, long ms) {
        BendableScore score = scorer.update(plan);
        Kpis k = Kpis.of(plan, load.asOf().getEpochSecond() / 60, load.horizonEnd().getEpochSecond() / 60);
        return new Result(facilityId, method, score.isFeasible(), score.toString(), ms, k);
    }
}
