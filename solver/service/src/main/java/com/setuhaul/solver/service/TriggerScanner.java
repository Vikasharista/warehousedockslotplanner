package com.setuhaul.solver.service;

import io.quarkus.logging.Log;
import io.quarkus.scheduler.Scheduled;

import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;

import org.eclipse.microprofile.config.inject.ConfigProperty;

import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.Duration;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

import javax.sql.DataSource;

/**
 * Decides when a facility needs a re-plan (plan of record, "Triggers"), checked on a timer against the shared clock:
 * <ul>
 * <li>CHECKIN: a truck reached the gate since the last run;</li>
 * <li>NEW_REQUEST: a truck request for the facility was made since the last run;</li>
 * <li>ETA_DELAY: a booked truck's effective ETA (GPS included) moved more than the threshold, later or earlier,
 * from the ETA its slot was planned on;</li>
 * <li>SCHEDULED: no run for the scheduled interval, or no run yet.</li>
 * </ul>
 * Event triggers wait a minimum gap after the last run, so a plan that cannot commit does not re-run every scan.
 */
@ApplicationScoped
public class TriggerScanner {

    private static final String CHECKINS = """
            SELECT shipment_id FROM facility_checkins
            WHERE facility_id = ? AND gate_in_ts > ? AND gate_in_ts <= ?
            ORDER BY gate_in_ts, shipment_id
            """;

    private static final String NEW_REQUESTS = """
            SELECT shipment_id FROM shipments
            WHERE destination_facility_id = ? AND created_at > ? AND created_at <= ?
              AND current_status NOT IN ('COMPLETED', 'CANCELLED')
            ORDER BY created_at, shipment_id
            """;

    private static final String ETA_SHIFTS = """
            SELECT i.shipment_id,
                   round(EXTRACT(EPOCH FROM e.eta_ts - i.eta_basis_ts) / 60)::int AS shift_min
            FROM solver_plan_item i
            CROSS JOIN LATERAL effective_eta(i.shipment_id, ?) e
            LEFT JOIN facility_checkins fc ON fc.shipment_id = i.shipment_id AND fc.gate_in_ts <= ?
            WHERE i.run_id = ? AND i.allocation_status = 'ALLOCATED' AND i.eta_basis_ts IS NOT NULL
              AND fc.shipment_id IS NULL
              AND abs(EXTRACT(EPOCH FROM e.eta_ts - i.eta_basis_ts)) / 60 > ?
            ORDER BY i.shipment_id
            """;

    /** What to run and why; empty when nothing is due. */
    public record Decision(String facilityId, String trigger, String detail) {
    }

    @Inject
    DataSource dataSource;

    @Inject
    ReplanService replans;

    @ConfigProperty(name = "setuhaul.triggers.enabled", defaultValue = "true")
    boolean enabled;

    @ConfigProperty(name = "setuhaul.triggers.facilities", defaultValue = "FAC-JAI-01")
    List<String> facilities;

    @ConfigProperty(name = "setuhaul.triggers.eta-shift-min", defaultValue = "30")
    int etaShiftMin;

    @ConfigProperty(name = "setuhaul.triggers.schedule-min", defaultValue = "15")
    int scheduleMin;

    @ConfigProperty(name = "setuhaul.triggers.min-gap-min", defaultValue = "2")
    int minGapMin;

    @Scheduled(every = "${setuhaul.triggers.scan-every:60s}", delayed = "${setuhaul.triggers.first-scan-after:10s}",
            concurrentExecution = Scheduled.ConcurrentExecution.SKIP)
    void scan() {
        if (!enabled) {
            return;
        }
        for (String facility : facilities) {
            try {
                Optional<Decision> d = decide(facility);
                if (d.isPresent()) {
                    Log.infof("re-plan %s: %s (%s)", facility, d.get().trigger(), d.get().detail());
                    Api.ReplanResult r = replans.replan(new Api.ReplanRequest(facility, d.get().trigger(), d.get().detail()));
                    Log.infof("re-plan %s finished: %s, run %s", facility, r.status(), r.finalRunId());
                }
            } catch (Exception e) {
                Log.errorf(e, "trigger scan of %s failed", facility);
            }
        }
    }

    /** The trigger due for a facility now, by priority CHECKIN, NEW_REQUEST, ETA_DELAY, SCHEDULED. */
    public Optional<Decision> decide(String facilityId) throws SQLException {
        try (Connection c = dataSource.getConnection()) {
            OffsetDateTime now = CommitService.now(c);
            Optional<OffsetDateTime> lastAsOf = one(c,
                    "SELECT as_of_ts FROM solver_run WHERE facility_id = ? ORDER BY created_at DESC LIMIT 1", facilityId);
            if (lastAsOf.isEmpty()) {
                return Optional.of(new Decision(facilityId, "SCHEDULED", "first plan"));
            }
            long sinceLast = Duration.between(lastAsOf.get(), now).toMinutes();
            if (sinceLast >= minGapMin) {
                List<String> arrived = new ArrayList<>();
                try (PreparedStatement ps = c.prepareStatement(CHECKINS)) {
                    ps.setString(1, facilityId);
                    ps.setObject(2, lastAsOf.get());
                    ps.setObject(3, now);
                    try (ResultSet rs = ps.executeQuery()) {
                        while (rs.next()) {
                            arrived.add(rs.getString(1));
                        }
                    }
                }
                if (!arrived.isEmpty()) {
                    return Optional.of(new Decision(facilityId, "CHECKIN", String.join(", ", arrived) + " at the gate"));
                }
                List<String> requested = new ArrayList<>();
                try (PreparedStatement ps = c.prepareStatement(NEW_REQUESTS)) {
                    ps.setString(1, facilityId);
                    ps.setObject(2, lastAsOf.get());
                    ps.setObject(3, now);
                    try (ResultSet rs = ps.executeQuery()) {
                        while (rs.next()) {
                            requested.add(rs.getString(1));
                        }
                    }
                }
                if (!requested.isEmpty()) {
                    String shown = String.join(", ", requested.subList(0, Math.min(5, requested.size())))
                            + (requested.size() > 5 ? " and " + (requested.size() - 5) + " more" : "");
                    return Optional.of(new Decision(facilityId, "NEW_REQUEST", requested.size() + " new: " + shown));
                }
                Optional<String> committed = oneString(c, """
                        SELECT run_id FROM solver_run WHERE facility_id = ? AND status = 'COMMITTED'
                        ORDER BY committed_at DESC LIMIT 1
                        """, facilityId);
                if (committed.isPresent()) {
                    List<String> shifts = new ArrayList<>();
                    try (PreparedStatement ps = c.prepareStatement(ETA_SHIFTS)) {
                        ps.setObject(1, now);
                        ps.setObject(2, now);
                        ps.setString(3, committed.get());
                        ps.setInt(4, etaShiftMin);
                        try (ResultSet rs = ps.executeQuery()) {
                            while (rs.next()) {
                                int shift = rs.getInt(2);
                                shifts.add(rs.getString(1) + " " + (shift > 0 ? "+" : "") + shift + " min");
                            }
                        }
                    }
                    if (!shifts.isEmpty()) {
                        return Optional.of(new Decision(facilityId, "ETA_DELAY", "ETA moved: " + String.join(", ", shifts)));
                    }
                }
            }
            if (sinceLast >= scheduleMin) {
                return Optional.of(new Decision(facilityId, "SCHEDULED", "every " + scheduleMin + " min"));
            }
            return Optional.empty();
        }
    }

    private static Optional<OffsetDateTime> one(Connection c, String sql, String arg) throws SQLException {
        try (PreparedStatement ps = c.prepareStatement(sql)) {
            ps.setString(1, arg);
            try (ResultSet rs = ps.executeQuery()) {
                return rs.next() ? Optional.ofNullable(rs.getObject(1, OffsetDateTime.class)) : Optional.empty();
            }
        }
    }

    private static Optional<String> oneString(Connection c, String sql, String arg) throws SQLException {
        try (PreparedStatement ps = c.prepareStatement(sql)) {
            ps.setString(1, arg);
            try (ResultSet rs = ps.executeQuery()) {
                return rs.next() ? Optional.ofNullable(rs.getString(1)) : Optional.empty();
            }
        }
    }
}
