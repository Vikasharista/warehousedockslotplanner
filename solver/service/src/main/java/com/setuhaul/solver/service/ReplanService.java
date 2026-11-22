package com.setuhaul.solver.service;

import io.quarkus.logging.Log;

import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;

import org.eclipse.microprofile.config.inject.ConfigProperty;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.locks.ReentrantLock;

/**
 * One re-plan of a facility as of the shared clock: solve, store, commit. When the commit loses a race (inputs
 * changed or another writer took a slot) the facility is re-planned straight away, up to the configured attempts.
 * Re-plans of one facility never overlap; commits are also serialised in the database.
 */
@ApplicationScoped
public class ReplanService {

    @Inject
    PlanService plans;

    @Inject
    CommitService commits;

    @ConfigProperty(name = "setuhaul.replan.auto-commit", defaultValue = "true")
    boolean autoCommit;

    @ConfigProperty(name = "setuhaul.replan.max-attempts", defaultValue = "2")
    int maxAttempts;

    @ConfigProperty(name = "setuhaul.replan.wait-seconds", defaultValue = "120")
    int waitSeconds;

    private final ConcurrentHashMap<String, ReentrantLock> locks = new ConcurrentHashMap<>();

    public Api.ReplanResult replan(Api.ReplanRequest req) throws Exception {
        if (req == null || req.facilityId() == null || req.facilityId().isBlank()) {
            throw new IllegalArgumentException("facilityId is required");
        }
        String trigger = req.trigger() == null ? "MANUAL" : req.trigger().toUpperCase(Locale.ROOT);
        ReentrantLock lock = locks.computeIfAbsent(req.facilityId(), k -> new ReentrantLock());
        if (!lock.tryLock(waitSeconds, TimeUnit.SECONDS)) {
            throw new CommitService.Conflict("a re-plan of " + req.facilityId() + " is still running");
        }
        try {
            List<Api.CommitResult> attempts = new ArrayList<>();
            String detail = req.detail();
            for (int attempt = 1; attempt <= Math.max(1, maxAttempts); attempt++) {
                Api.Run run = plans.solve(new Api.SolveRequest(req.facilityId(), null, trigger, null), detail);
                if (!run.feasible()) {
                    attempts.add(new Api.CommitResult(run.runId(), run.status(), run.outcome(), false, 0, 0, List.of()));
                    return new Api.ReplanResult(req.facilityId(), "REJECTED", run.runId(), attempts);
                }
                if (!autoCommit) {
                    attempts.add(new Api.CommitResult(run.runId(), "SOLVED", "auto-commit is off", false, 0, 0, List.of()));
                    return new Api.ReplanResult(req.facilityId(), "SOLVED", run.runId(), attempts);
                }
                Api.CommitResult result = commits.commit(run.runId());
                attempts.add(result);
                if (!result.retry()) {
                    return new Api.ReplanResult(req.facilityId(), result.status(), run.runId(), attempts);
                }
                detail = "retry after " + result.outcome();
                Log.infof("re-planning %s again: %s", req.facilityId(), result.outcome());
            }
            Api.CommitResult last = attempts.get(attempts.size() - 1);
            return new Api.ReplanResult(req.facilityId(), last.status(), last.runId(), attempts);
        } finally {
            lock.unlock();
        }
    }
}
