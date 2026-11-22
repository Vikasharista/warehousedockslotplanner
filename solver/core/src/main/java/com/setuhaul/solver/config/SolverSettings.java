package com.setuhaul.solver.config;

import java.io.IOException;
import java.io.InputStream;
import java.io.Reader;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Locale;
import java.util.Map;
import java.util.Properties;
import java.util.Set;
import java.util.stream.Collectors;
import java.util.stream.Stream;

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
        Bumping bumping,
        long solveSeconds,
        long unimprovedSeconds,
        long randomSeed,
        String warmStart,
        int earlyReleaseMin) {

    /** Where the search starts: an empty plan, or a plan built by one of the baseline rules. */
    public static final Set<String> WARM_STARTS = Set.of("NONE", "FCFS", "PRIORITY_FIRST");

    public static final String DEFAULTS_RESOURCE = "/solver-defaults.properties";

    /**
     * Which trucks may take a confirmed booking's slot, from whom, and at what cost.
     *
     * @param enabled    off: every feasible confirmed booking stays pinned
     * @param by         priorities that may bump (CRITICAL)
     * @param victims    priorities of bookings that may be bumped (LOW, NORMAL)
     * @param freezeMin  bookings starting within this many minutes of the plan are never bumped
     * @param penalty    cost of bumping one booking, on the unassigned-truck level (L1)
     */
    public record Bumping(boolean enabled, Set<String> by, Set<String> victims, int freezeMin, long penalty) {
        public Bumping {
            by = Set.copyOf(by);
            victims = Set.copyOf(victims);
        }

        public static Bumping off() {
            return new Bumping(false, Set.of(), Set.of(), 0, 0);
        }
    }

    public SolverSettings {
        priorityWeights = Map.copyOf(priorityWeights);
        for (String p : new String[] {"LOW", "NORMAL", "HIGH", "CRITICAL"}) {
            if (!priorityWeights.containsKey(p)) {
                throw new IllegalArgumentException("missing priority_weight." + p);
            }
        }
        if (gateBufferMin < 0 || lowConfidenceMarginMin < 0 || horizonHours <= 0 || holdExpiryMin < 0
                || inDockOverrunMin < 0 || defaultUnloadMin <= 0 || earlyReleaseMin <= 0) {
            throw new IllegalArgumentException("buffers must be >= 0, horizon and unload > 0");
        }
        if (bumping == null) {
            bumping = Bumping.off();
        }
        warmStart = warmStart == null ? "NONE" : warmStart.trim().toUpperCase(Locale.ROOT);
        if (!WARM_STARTS.contains(warmStart)) {
            throw new IllegalArgumentException("warm_start must be one of " + WARM_STARTS);
        }
        if (bumping.enabled()) {
            if (bumping.by().isEmpty() || bumping.victims().isEmpty() || bumping.freezeMin() < 0) {
                throw new IllegalArgumentException("bumping needs bump_by and bump_victims, and bump_freeze_min >= 0");
            }
            for (String p : bumping.by()) {
                if (bumping.victims().contains(p)) {
                    throw new IllegalArgumentException("priority " + p + " cannot both bump and be bumped");
                }
                // Placing a bumping truck must be worth the bump...
                if (priorityWeights.getOrDefault(p, 0) <= bumping.penalty()) {
                    throw new IllegalArgumentException("bump_penalty must be below the weight of " + p);
                }
            }
            for (var e : priorityWeights.entrySet()) {
                // ...and placing any other truck must not be.
                if (!bumping.by().contains(e.getKey()) && e.getValue() > bumping.penalty()) {
                    throw new IllegalArgumentException("bump_penalty must be at least the weight of " + e.getKey()
                            + " (" + e.getValue() + "), or " + e.getKey() + " trucks could justify a bump");
                }
            }
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
                bumping, seconds, Math.min(unimprovedSeconds, seconds), randomSeed, warmStart, earlyReleaseMin);
    }

    /** The same settings with a different search start (NONE, FCFS or PRIORITY_FIRST). */
    public SolverSettings withWarmStart(String start) {
        return new SolverSettings(gateBufferMin, lowConfidenceMarginMin, horizonHours, inDockOverrunMin, holdExpiryMin,
                defaultUnloadMin, priorityWeights, unforcedMovePenalty, forcedMovePenalty, pinFeasibleConfirmed,
                bumping, solveSeconds, unimprovedSeconds, randomSeed, start, earlyReleaseMin);
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
                new Bumping(Boolean.parseBoolean(required(p, "bump_enabled")), setOf(p, "bump_by"),
                        setOf(p, "bump_victims"), intOf(p, "bump_freeze_min"), longOf(p, "bump_penalty")),
                longOf(p, "solve_seconds"),
                longOf(p, "unimproved_seconds"),
                longOf(p, "random_seed"),
                required(p, "warm_start"),
                intOf(p, "early_release_min"));
    }

    private static String required(Properties p, String key) {
        String v = p.getProperty(key);
        if (v == null || v.isBlank()) {
            throw new IllegalArgumentException("missing setting " + key);
        }
        return v.trim();
    }

    private static Set<String> setOf(Properties p, String key) {
        return Stream.of(required(p, key).split(",")).map(s -> s.trim().toUpperCase(Locale.ROOT))
                .filter(s -> !s.isEmpty()).collect(Collectors.toUnmodifiableSet());
    }

    private static int intOf(Properties p, String key) {
        return Integer.parseInt(required(p, key));
    }

    private static long longOf(Properties p, String key) {
        return Long.parseLong(required(p, key));
    }
}
