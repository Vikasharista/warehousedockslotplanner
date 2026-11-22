package com.setuhaul.solver.service;

import ai.timefold.solver.core.api.solver.SolverConfigOverride;
import ai.timefold.solver.core.api.solver.SolverFactory;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.load.LoadResult;
import com.setuhaul.solver.load.PostgresLoader;
import com.setuhaul.solver.report.Kpis;
import com.setuhaul.solver.solve.DockSolver;

import io.quarkus.logging.Log;

import jakarta.annotation.PostConstruct;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;

import org.eclipse.microprofile.config.inject.ConfigProperty;

import java.nio.file.Path;
import java.sql.Connection;
import java.time.Duration;
import java.time.OffsetDateTime;
import java.util.List;
import java.util.Locale;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;

import javax.sql.DataSource;

/** Loads a facility as of a time, solves it, and stores the run. Writing bookings is {@link CommitService}'s job. */
@ApplicationScoped
public class PlanService {

    static final Set<String> TRIGGERS = Set.of("SCHEDULED", "ETA_DELAY", "CHECKIN", "NEW_REQUEST", "MANUAL");
    static final int MAX_SOLVE_SECONDS = 60;

    @Inject
    DataSource dataSource;

    @Inject
    SolverFactory<DockPlan> solverFactory;

    @Inject
    RunRepository runs;

    @Inject
    ObjectMapper json;

    @ConfigProperty(name = "setuhaul.solver.settings-file")
    Optional<String> settingsFile;

    SolverSettings settings;

    @PostConstruct
    void init() throws Exception {
        settings = settingsFile.isPresent()
                ? SolverSettings.withOverrides(Path.of(settingsFile.get()))
                : SolverSettings.defaults();
        Log.infof("solver settings: %s", settings);
    }

    public Api.Run solve(Api.SolveRequest req) throws Exception {
        return solve(req, null);
    }

    /** Solves and stores a run (status SOLVED or REJECTED). With no asOf the shared clock's now is used. */
    public Api.Run solve(Api.SolveRequest req, String triggerDetail) throws Exception {
        if (req == null || req.facilityId() == null || req.facilityId().isBlank()) {
            throw new IllegalArgumentException("facilityId is required");
        }
        OffsetDateTime asOf = req.asOf();
        if (asOf == null) {
            try (Connection c = dataSource.getConnection()) {
                asOf = CommitService.now(c);
            }
        }
        String trigger = req.trigger() == null ? "MANUAL" : req.trigger().toUpperCase(Locale.ROOT);
        if (!TRIGGERS.contains(trigger)) {
            throw new IllegalArgumentException("trigger must be one of " + TRIGGERS);
        }
        SolverSettings effective = settings;
        if (req.solveSeconds() != null) {
            if (req.solveSeconds() < 1 || req.solveSeconds() > MAX_SOLVE_SECONDS) {
                throw new IllegalArgumentException("solveSeconds must be between 1 and " + MAX_SOLVE_SECONDS);
            }
            effective = settings.withSolveSeconds(req.solveSeconds());
        }

        LoadResult load;
        try (Connection c = dataSource.getConnection()) {
            load = new PostgresLoader(effective).load(c, req.facilityId(), asOf);
        }

        long started = System.nanoTime();
        var termination = DockSolver.timeLimited(effective);
        DockPlan solved = solverFactory
                .buildSolver(new SolverConfigOverride().withTerminationConfig(termination))
                .solve(load.plan());
        long solveMs = Duration.ofNanos(System.nanoTime() - started).toMillis();

        boolean feasible = solved.getScore() != null && solved.getScore().isFeasible();
        Kpis kpis = Kpis.of(solved, load.asOf().getEpochSecond() / 60, load.horizonEnd().getEpochSecond() / 60);
        // The run records the exact as-of time the inputs were read at: the commit's stale check starts there.
        Api.Run run = new Api.Run("RUN-" + UUID.randomUUID(), req.facilityId(),
                asOf.atZoneSameInstant(load.zone()).toOffsetDateTime(),
                load.horizonEnd().atZone(load.zone()).toOffsetDateTime(),
                trigger, triggerDetail, feasible ? "SOLVED" : "REJECTED", feasible, String.valueOf(solved.getScore()),
                solveMs, kpis, null, feasible ? null : "INFEASIBLE: " + solved.getScore(), List.of(),
                PlanItems.of(load, solved));
        // The run keeps the exact settings it was solved with, so the validator and audits can recompute its rules.
        runs.save(run, json.writeValueAsString(effective));
        Log.infof("run %s for %s as of %s: %s, %d assigned, %d unassigned, %d ms",
                run.runId(), run.facilityId(), run.asOf(), run.score(), kpis.assigned(), kpis.unassigned(), solveMs);
        return run;
    }
}
