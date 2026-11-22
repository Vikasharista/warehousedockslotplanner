package com.setuhaul.solver.cli;

import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.load.LoadResult;
import com.setuhaul.solver.load.PostgresLoader;
import com.setuhaul.solver.report.PlanReport;
import com.setuhaul.solver.solve.DockSolver;

import java.nio.file.Path;
import java.sql.Connection;
import java.sql.DriverManager;
import java.time.OffsetDateTime;
import java.util.HashMap;
import java.util.Map;

/**
 * Solves one facility at one as-of time and prints the plan. Reads Postgres in a read-only transaction and writes
 * nothing.
 *
 * <pre>
 * java -jar target/dock-solver.jar --jdbc-url jdbc:postgresql://localhost:55432/setuhaul?user=postgres&amp;password=postgres \
 *      --facility FAC-JAI-01 --as-of 2026-08-04T09:55:00+05:30 [--config my.properties] [--seconds 10]
 * </pre>
 * {@code SETUHAUL_JDBC_URL} is used when {@code --jdbc-url} is not given.
 */
public final class DryRun {

    private DryRun() {
    }

    public static void main(String[] args) throws Exception {
        Map<String, String> opts = parse(args);
        String url = opts.getOrDefault("jdbc-url", System.getenv("SETUHAUL_JDBC_URL"));
        if (url == null || opts.containsKey("help") || !opts.containsKey("as-of")) {
            System.err.println("usage: --as-of <ISO time with offset> [--facility FAC-JAI-01] [--jdbc-url URL] "
                    + "[--config file.properties] [--seconds N]");
            System.exit(2);
        }
        SolverSettings settings = opts.containsKey("config")
                ? SolverSettings.withOverrides(Path.of(opts.get("config")))
                : SolverSettings.defaults();
        if (opts.containsKey("seconds")) {
            settings = settings.withSolveSeconds(Long.parseLong(opts.get("seconds")));
        }
        String facility = opts.getOrDefault("facility", "FAC-JAI-01");
        OffsetDateTime asOf = OffsetDateTime.parse(opts.get("as-of"));

        LoadResult load;
        try (Connection conn = DriverManager.getConnection(url)) {
            load = new PostgresLoader(settings).load(conn, facility, asOf);
        }
        DockPlan solved = DockSolver.solve(load.plan(), settings, DockSolver.timeLimited(settings));
        System.out.print(PlanReport.render(load, solved));
    }

    static Map<String, String> parse(String[] args) {
        Map<String, String> m = new HashMap<>();
        for (int i = 0; i < args.length; i++) {
            if (!args[i].startsWith("--")) {
                throw new IllegalArgumentException("unexpected argument " + args[i]);
            }
            String key = args[i].substring(2);
            if (key.equals("help")) {
                m.put(key, "true");
            } else if (i + 1 < args.length) {
                m.put(key, args[++i]);
            } else {
                throw new IllegalArgumentException("missing value for --" + key);
            }
        }
        return m;
    }
}
