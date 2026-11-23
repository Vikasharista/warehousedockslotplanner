package com.setuhaul.solver;

import com.setuhaul.solver.cli.Compare;
import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.scale.ScaleRun;

import org.junit.jupiter.api.Test;

import java.sql.Connection;
import java.sql.DriverManager;
import java.util.ArrayList;
import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

/**
 * The plan's scale test: 240 shipments, 32 docks, 6 facilities, each facility planned on its own as in production,
 * must reach a feasible plan inside the 60-second limit. Needs SETUHAUL_SCALE_JDBC_URL pointing at a database built
 * with {@code python -m setuhaul_jobs.scale} (defaults: 6 facilities, 32 docks, 240 shipments); skipped otherwise.
 */
class ScaleIT {

    static final long LIMIT_SECONDS = 60;

    @Test
    void everyFacilityGetsAFeasiblePlanWithinTheLimit() throws Exception {
        String url = System.getenv("SETUHAUL_SCALE_JDBC_URL");
        assumeTrue(url != null && !url.isBlank(), "SETUHAUL_SCALE_JDBC_URL is not set");
        List<ScaleRun.Result> results = new ArrayList<>();
        try (Connection c = DriverManager.getConnection(url)) {
            for (String f : Compare.facilities(c, "FAC-SCL-*")) {
                results.add(ScaleRun.run(c, f, SnapshotSolveIT.MORNING, SolverSettings.defaults()));
            }
        }
        assertThat(results).hasSize(6);
        assertThat(results.stream().mapToInt(ScaleRun.Result::docks).sum()).isEqualTo(32);
        assertThat(results.stream().mapToInt(ScaleRun.Result::trucks).sum())
                .as("240 requests less the weight breaches flagged for a coordinator").isBetween(220, 240);
        for (ScaleRun.Result r : results) {
            assertThat(r.passes(LIMIT_SECONDS)).as("%s: %s in %d ms", r.facilityId(), r.score(), r.solveMs()).isTrue();
            assertThat(r.firstFeasibleMs()).as(r.facilityId()).isBetween(0L, LIMIT_SECONDS * 1000);
        }
    }
}
