package com.setuhaul.solver.cli;

import ai.timefold.solver.core.config.solver.termination.TerminationConfig;

import com.setuhaul.solver.baseline.Comparison;
import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.solve.DockSolver;

import java.nio.file.Files;
import java.nio.file.Path;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.stream.Collectors;

/**
 * Compares the solver with the baseline rules (first come first served, priority first) on the same data and prints
 * a table. Reads Postgres only.
 *
 * <pre>
 * java -cp target/dock-solver.jar com.setuhaul.solver.cli.Compare --jdbc-url URL --as-of 2026-08-04T09:55:00+05:30 \
 *      [--facility FAC-JAI-01 | --facility all] [--config file.properties] [--seconds N | --steps N] [--json out.json]
 * </pre>
 * {@code --facility all} (or a prefix such as {@code 'FAC-SCL-*'}) compares every matching facility that has slots, one
 * solver run each (as in production).
 */
public final class Compare {

    private Compare() {
    }

    public static void main(String[] args) throws Exception {
        var opts = DryRun.parse(args);
        String url = opts.getOrDefault("jdbc-url", System.getenv("SETUHAUL_JDBC_URL"));
        if (url == null || !opts.containsKey("as-of")) {
            System.err.println("usage: --as-of <ISO time with offset> [--facility ID|all] [--jdbc-url URL] "
                    + "[--config file.properties] [--seconds N | --steps N] [--json file]");
            System.exit(2);
        }
        SolverSettings settings = opts.containsKey("config")
                ? SolverSettings.withOverrides(Path.of(opts.get("config")))
                : SolverSettings.defaults();
        if (opts.containsKey("seconds")) {
            settings = settings.withSolveSeconds(Long.parseLong(opts.get("seconds")));
        }
        TerminationConfig termination = opts.containsKey("steps")
                ? DockSolver.stepLimited(Integer.parseInt(opts.get("steps")))
                : DockSolver.timeLimited(settings);
        OffsetDateTime asOf = OffsetDateTime.parse(opts.get("as-of"));

        List<Comparison.Result> all = new ArrayList<>();
        try (Connection conn = DriverManager.getConnection(url)) {
            for (String facility : facilities(conn, opts.getOrDefault("facility", "FAC-JAI-01"))) {
                all.addAll(Comparison.run(conn, facility, asOf, settings, termination));
            }
        }
        System.out.print(table(all));
        if (opts.containsKey("json")) {
            Files.writeString(Path.of(opts.get("json")),
                    all.stream().map(Comparison.Result::toJson).collect(Collectors.joining(",\n", "[\n", "\n]\n")));
        }
    }

    /** Facility ids: one id, every facility with slots ("all"), or those starting with a prefix ("FAC-SCL-*"). */
    public static List<String> facilities(Connection conn, String which) throws Exception {
        if (!which.equalsIgnoreCase("all") && !which.endsWith("*")) {
            return List.of(which);
        }
        String like = which.equalsIgnoreCase("all") ? "%" : which.substring(0, which.length() - 1) + "%";
        List<String> out = new ArrayList<>();
        try (PreparedStatement ps = conn.prepareStatement(
                "SELECT DISTINCT facility_id FROM appointment_slots WHERE facility_id LIKE ? ORDER BY facility_id")) {
            ps.setString(1, like);
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    out.add(rs.getString(1));
                }
            }
        }
        return out;
    }

    static String table(List<Comparison.Result> rows) {
        StringBuilder sb = new StringBuilder(
                "| Facility | Method | Feasible | Placed | Avg wait | Weighted wait | Inversions | Pickup inv. | "
                        + "Unforced moves | Utilisation | ms |\n|---|---|---|---|---|---|---|---|---|---|---|\n");
        for (Comparison.Result r : rows) {
            var k = r.kpis();
            sb.append(String.format(Locale.ROOT, "| %s | %s | %s | %d/%d | %.1f | %.1f | %d | %d | %d | %.1f%% | %d |%n",
                    r.facilityId(), r.method(), r.feasible() ? "yes" : "NO", k.assigned(), k.trucksPlanned(),
                    k.averageWaitMin(), k.priorityWeightedWaitMin(), k.priorityInversions(), k.pickupInversions(),
                    k.unforcedMoves(), k.dockUtilisationPct(), r.planMs()));
        }
        return sb.toString();
    }
}
