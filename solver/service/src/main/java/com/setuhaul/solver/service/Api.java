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
     * @param trigger      SCHEDULED, ETA_DELAY, CHECKIN or MANUAL (default MANUAL)
     * @param solveSeconds optional search time limit, 1 to 60; defaults to the configured value
     */
    public record SolveRequest(String facilityId, OffsetDateTime asOf, String trigger, Integer solveSeconds) {
    }

    public record Run(
            String runId,
            String facilityId,
            OffsetDateTime asOf,
            OffsetDateTime horizonEnd,
            String trigger,
            String status,
            boolean feasible,
            String score,
            long solveMs,
            Kpis kpis,
            List<Item> items) {
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
            Integer predictedWaitMin,
            String bookingAction,
            String originalSlotId,
            boolean pinned,
            String reasonCode,
            String detail) {
    }

    public record Error(String error) {
    }
}
