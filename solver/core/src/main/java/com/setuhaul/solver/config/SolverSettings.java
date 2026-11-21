package com.setuhaul.solver.config;

import java.io.IOException;
import java.io.InputStream;
import java.io.Reader;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Locale;
import java.util.Map;
import java.util.Properties;

/**
 * Every weight, buffer and limit the solver uses. Defaults come from {@code solver-defaults.properties};
 * a file passed with {@code --config} overrides any of them.
 */
public record SolverSettings(
        int gateBufferMin,
        int lowConfidenceMarginMin,
        int horizonHours,
        int inDockOverrunMin,
        int holdExpiryMin,
        int defaultUnloadMin,
        Map<String, Integer> priorityWeights,
        long unforcedMovePenalty,
        long forcedMovePenalty,
        boolean pinFeasibleConfirmed,
        long solveSeconds,
        long unimprovedSeconds,
        long randomSeed) {

    public static final String DEFAULTS_RESOURCE = "/solver-defaults.properties";

    public SolverSettings {
        priorityWeights = Map.copyOf(priorityWeights);
        for (String p : new String[] {"LOW", "NORMAL", "HIGH", "CRITICAL"}) {
            if (!priorityWeights.containsKey(p)) {
                throw new IllegalArgumentException("missing priority_weight." + p);
            }
        }
        if (gateBufferMin < 0 || lowConfidenceMarginMin < 0 || horizonHours <= 0 || holdExpiryMin < 0
                || inDockOverrunMin < 0 || defaultUnloadMin <= 0) {
            throw new IllegalArgumentException("buffers must be >= 0, horizon and unload > 0");
        }
    }

    public int priorityWeight(String priorityCode) {
        Integer w = priorityWeights.get(priorityCode.toUpperCase(Locale.ROOT));
        if (w == null) {
            throw new IllegalArgumentException("unknown priority " + priorityCode);
        }
        return w;
    }

    /** The same settings with a different search time limit; the unimproved limit never exceeds it. */
    public SolverSettings withSolveSeconds(long seconds) {
        return new SolverSettings(gateBufferMin, lowConfidenceMarginMin, horizonHours, inDockOverrunMin, holdExpiryMin,
                defaultUnloadMin, priorityWeights, unforcedMovePenalty, forcedMovePenalty, pinFeasibleConfirmed,
                seconds, Math.min(unimprovedSeconds, seconds), randomSeed);
    }

    public static SolverSettings defaults() {
        return fromProperties(loadDefaults());
    }

    /** Defaults, then every key present in {@code overrideFile} replaces its default. */
    public static SolverSettings withOverrides(Path overrideFile) throws IOException {
        Properties p = loadDefaults();
        try (Reader r = Files.newBufferedReader(overrideFile)) {
            Properties o = new Properties();
            o.load(r);
            for (String key : o.stringPropertyNames()) {
                if (!p.containsKey(key)) {
                    throw new IllegalArgumentException("unknown setting '" + key + "' in " + overrideFile);
                }
                p.setProperty(key, o.getProperty(key));
            }
        }
        return fromProperties(p);
    }

    private static Properties loadDefaults() {
        Properties p = new Properties();
        try (InputStream in = SolverSettings.class.getResourceAsStream(DEFAULTS_RESOURCE)) {
            if (in == null) {
                throw new IllegalStateException(DEFAULTS_RESOURCE + " missing from the classpath");
            }
            p.load(in);
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
        return p;
    }

    static SolverSettings fromProperties(Properties p) {
        return new SolverSettings(
                intOf(p, "gate_buffer_min"),
                intOf(p, "low_confidence_margin_min"),
                intOf(p, "horizon_hours"),
                intOf(p, "in_dock_overrun_min"),
                intOf(p, "hold_expiry_min"),
                intOf(p, "default_unload_min"),
                Map.of(
                        "LOW", intOf(p, "priority_weight.LOW"),
                        "NORMAL", intOf(p, "priority_weight.NORMAL"),
                        "HIGH", intOf(p, "priority_weight.HIGH"),
                        "CRITICAL", intOf(p, "priority_weight.CRITICAL")),
                longOf(p, "unforced_move_penalty"),
                longOf(p, "forced_move_penalty"),
                Boolean.parseBoolean(required(p, "pin_feasible_confirmed")),
                longOf(p, "solve_seconds"),
                longOf(p, "unimproved_seconds"),
                longOf(p, "random_seed"));
    }

    private static String required(Properties p, String key) {
        String v = p.getProperty(key);
        if (v == null || v.isBlank()) {
            throw new IllegalArgumentException("missing setting " + key);
        }
        return v.trim();
    }

    private static int intOf(Properties p, String key) {
        return Integer.parseInt(required(p, key));
    }

    private static long longOf(Properties p, String key) {
        return Long.parseLong(required(p, key));
    }
}
