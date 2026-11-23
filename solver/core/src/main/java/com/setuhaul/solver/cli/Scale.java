package com.setuhaul.solver.cli;

import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.scale.ScaleRun;

import java.nio.file.Files;
import java.nio.file.Path;
import java.sql.Connection;
import java.sql.DriverManager;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.stream.Collectors;

/**
 * The scale test: plans each facility on its own, as the service does, and checks each reaches a feasible plan
 * within the limit. Reads Postgres only. Exit code 1 when any facility fails.
 *
 * <pre>
 * java -cp target/dock-solver.jar com.setuhaul.solver.cli.Scale --jdbc-url URL --as-of 2026-08-04T09:55:00+05:30 \
 *      [--facility 'FAC-SCL-*'] [--config file.properties] [--seconds N] [--limit-seconds 60] [--json out.json]
 * </pre>
 */
public final class Scale {

    private Scale() {
    }

    public static void main(String[] args) throws Exception {
        var opts = DryRun.parse(args);
        String url = opts.getOrDefault("jdbc-url", System.getenv("SETUHAUL_JDBC_URL"));
        if (url == null || !opts.containsKey("as-of")) {
            System.err.println("usage: --as-of <ISO time> [--facility 'FAC-SCL-*'] [--jdbc-url URL] [--config file] "
                    + "[--seconds N] [--limit-seconds 60] [--json file]");
            System.exit(2);
        }
        SolverSettings settings = opts.containsKey("config")
                ? SolverSettings.withOverrides(Path.of(opts.get("config")))
                : SolverSettings.defaults();
        if (opts.containsKey("seconds")) {
            settings = settings.withSolveSeconds(Long.parseLong(opts.get("seconds")));
        }
        long limit = Long.parseLong(opts.getOrDefault("limit-seconds", "60"));
        OffsetDateTime asOf = OffsetDateTime.parse(opts.get("as-of"));

        List<ScaleRun.Result> results = new ArrayList<>();
        try (Connection conn = DriverManager.getConnection(url)) {
            for (String f : Compare.facilities(conn, opts.getOrDefault("facility", "FAC-SCL-*"))) {
                results.add(ScaleRun.run(conn, f, asOf, settings));
            }
        }
        System.out.print(table(results, limit));
        if (opts.containsKey("json")) {
            Files.writeString(Path.of(opts.get("json")), results.stream().map(ScaleRun.Result::toJson)
                    .collect(Collectors.joining(",\n", "[\n", "\n]\n")));
        }
        System.exit(results.stream().allMatch(r -> r.passes(limit)) ? 0 : 1);
    }

    static String table(List<ScaleRun.Result> rows, long limit) {
        StringBuilder sb = new StringBuilder("| Facility | Docks | Slots | Trucks | Load ms | First feasible ms | Best at ms | "
                + "Solve ms | Placed | Weighted wait | Inversions | Utilisation | Result |\n"
                + "|---|---|---|---|---|---|---|---|---|---|---|---|---|\n");
        int trucks = 0;
        int docks = 0;
        long worst = 0;
        for (ScaleRun.Result r : rows) {
            trucks += r.trucks();
            docks += r.docks();
            worst = Math.max(worst, r.solveMs());
            sb.append(String.format(Locale.ROOT, "| %s | %d | %d | %d | %d | %d | %d | %d | %d/%d | %.1f | %d | %.1f%% | %s |%n",
                    r.facilityId(), r.docks(), r.slots(), r.trucks(), r.loadMs(), r.firstFeasibleMs(), r.bestMs(),
                    r.solveMs(), r.kpis().assigned(), r.trucks(), r.kpis().priorityWeightedWaitMin(),
                    r.kpis().priorityInversions(), r.kpis().dockUtilisationPct(), r.passes(limit) ? "pass" : "FAIL"));
        }
        sb.append(String.format(Locale.ROOT, "%n%d facilities, %d docks, %d trucks in the runs; slowest solve %d ms "
                + "(limit %d s per facility)%n", rows.size(), docks, trucks, worst, limit));
        return sb.toString();
    }
}
