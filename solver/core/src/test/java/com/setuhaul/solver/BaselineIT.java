package com.setuhaul.solver;

import ai.timefold.solver.core.api.score.BendableScore;

import com.setuhaul.solver.baseline.Comparison;
import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.ShipmentAssignment;
import com.setuhaul.solver.load.LoadResult;
import com.setuhaul.solver.load.PostgresLoader;
import com.setuhaul.solver.solve.DockSolver;

import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import java.sql.Connection;
import java.sql.DriverManager;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

/**
 * The baseline comparison and determinism on the seeded snapshot (plan section "Verification": baseline comparison,
 * determinism). Needs SETUHAUL_TEST_JDBC_URL; skipped otherwise.
 */
class BaselineIT {

    static String url;

    @BeforeAll
    static void needsDatabase() {
        url = System.getenv("SETUHAUL_TEST_JDBC_URL");
        assumeTrue(url != null && !url.isBlank(), "SETUHAUL_TEST_JDBC_URL is not set");
    }

    @Test
    void theSolverIsNeverWorseThanTheBaselineRulesOnTheScore() throws Exception {
        List<Comparison.Result> rows;
        try (Connection c = DriverManager.getConnection(url)) {
            rows = Comparison.run(c, SnapshotSolveIT.FACILITY, SnapshotSolveIT.MORNING, SolverSettings.defaults(),
                    DockSolver.stepLimited(2_000));
        }
        Map<String, Comparison.Result> by = new TreeMap<>();
        rows.forEach(r -> by.put(r.method(), r));
        assertThat(by).containsOnlyKeys("SOLVER", "SOLVER_COLD_START", "FCFS", "PRIORITY_FIRST");
        assertThat(rows).allMatch(Comparison.Result::feasible, "every method obeys the hard rules");

        BendableScore solver = BendableScore.parseScore(by.get("SOLVER").score());
        for (String rule : List.of("FCFS", "PRIORITY_FIRST")) {
            assertThat(solver.compareTo(BendableScore.parseScore(by.get(rule).score())))
                    .as("solver vs %s", rule).isGreaterThanOrEqualTo(0);
        }
        assertThat(by.get("PRIORITY_FIRST").kpis().priorityInversions()).isZero();
        assertThat(by.get("FCFS").kpis().priorityInversions()).as("FCFS ignores priority").isPositive();
        assertThat(by.get("SOLVER").kpis().priorityWeightedWaitMin())
                .isLessThan(by.get("FCFS").kpis().priorityWeightedWaitMin());
    }

    @Test
    void theSameSnapshotSeedAndStepLimitGiveTheSamePlan() throws Exception {
        for (String start : List.of("PRIORITY_FIRST", "NONE")) {
            SolverSettings s = SolverSettings.defaults().withWarmStart(start);
            Map<String, String> first = plan(s);
            Map<String, String> second = plan(s);
            assertThat(second).as("warm start %s", start).isEqualTo(first);
        }
    }

    /** Shipment to slot (or "-") plus the score, from a step-limited solve. */
    static Map<String, String> plan(SolverSettings s) throws Exception {
        LoadResult load;
        try (Connection c = DriverManager.getConnection(url)) {
            load = new PostgresLoader(s).load(c, SnapshotSolveIT.FACILITY, SnapshotSolveIT.MORNING);
        }
        DockPlan solved = DockSolver.solve(load.plan(), s, DockSolver.stepLimited(1_500));
        Map<String, String> out = new TreeMap<>();
        for (ShipmentAssignment a : solved.getAssignments()) {
            out.put(a.getShipmentId(), a.getSlot() == null ? "-" : a.getSlot().getId());
        }
        out.put("score", String.valueOf(solved.getScore()));
        return out;
    }
}
