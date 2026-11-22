package com.setuhaul.solver.service;

import com.setuhaul.solver.report.Kpis;

import java.time.OffsetDateTime;
import java.util.List;

/** Request and response bodies of the solver API. */
public final class Api {

    private Api() {
    }

    /**
     * @param facilityId   facility to plan, e.g. FAC-JAI-01
     * @param asOf         the moment the plan is made for, with offset, e.g. 2026-08-04T09:55:00+05:30
     * @param trigger      SCHEDULED, ETA_DELAY, CHECKIN, NEW_REQUEST or MANUAL (default MANUAL)
     * @param solveSeconds optional search time limit, 1 to 60; defaults to the configured value
     */
    public record SolveRequest(String facilityId, OffsetDateTime asOf, String trigger, Integer solveSeconds) {
    }

    /**
     * @param facilityId facility to re-plan now (as of the shared clock) and commit
     * @param trigger    SCHEDULED, ETA_DELAY, CHECKIN, NEW_REQUEST or MANUAL (default MANUAL)
     * @param detail     optional note on why, e.g. "SHP1012 +45 min"
     */
    public record ReplanRequest(String facilityId, String trigger, String detail) {
    }

    /**
     * @param committedAt set when the run's bookings were written to appointments
     * @param outcome     what the commit did, or why the run was not committed
     * @param violations  validator findings that stopped the commit
     */
    public record Run(
            String runId,
            String facilityId,
            OffsetDateTime asOf,
            OffsetDateTime horizonEnd,
            String trigger,
            String triggerDetail,
            String status,
            boolean feasible,
            String score,
            long solveMs,
            Kpis kpis,
            OffsetDateTime committedAt,
            String outcome,
            List<Violation> violations,
            List<Item> items) {

        Run withItems(List<Item> newItems) {
            return new Run(runId, facilityId, asOf, horizonEnd, trigger, triggerDetail, status, feasible, score,
                    solveMs, kpis, committedAt, outcome, violations, newItems);
        }
    }

    public record Violation(String checkId, String title, String shipmentId, String detail) {
    }

    /**
     * Result of a commit attempt. {@code retry} is true when the plan lost a race (inputs changed or another
     * writer took a slot) and a fresh re-plan should follow.
     */
    public record CommitResult(String runId, String status, String outcome, boolean retry, int written,
            int cancelled, List<Violation> violations) {
    }

    /** One re-plan: the runs it made (a lost race is re-planned once) and the final status. */
    public record ReplanResult(String facilityId, String status, String finalRunId, List<CommitResult> attempts) {
    }

    public record Item(
            String shipmentId,
            String allocationStatus,
            String vehicleId,
            String priorityCode,
            String dockId,
            String slotId,
            OffsetDateTime plannedStart,
            OffsetDateTime plannedEnd,
            Integer sequenceNo,
            OffsetDateTime earliestReady,
            OffsetDateTime etaBasis,
            Integer predictedWaitMin,
            String bookingAction,
            String originalSlotId,
            boolean pinned,
            String reasonCode,
            String detail,
            String appointmentId) {
    }

    public record Error(String error) {
    }
}
