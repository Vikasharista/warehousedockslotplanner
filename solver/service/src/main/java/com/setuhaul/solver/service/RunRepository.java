package com.setuhaul.solver.service;

import com.setuhaul.solver.report.Kpis;

import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;

import java.math.BigDecimal;
import java.math.RoundingMode;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Types;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

import javax.sql.DataSource;

/**
 * Stores and reads solver runs. A run and all its items are written in one transaction, so a reader never sees
 * half a plan. This class never touches appointments.
 */
@ApplicationScoped
public class RunRepository {

    private static final String INSERT_RUN = """
            INSERT INTO solver_run (run_id, facility_id, as_of_ts, horizon_end_ts, run_trigger, status, score, feasible,
                trucks_planned, assigned, unassigned, avg_wait_min, weighted_wait_min, priority_inversions,
                pickup_inversions, forced_moves, unforced_moves, dock_utilisation_pct, settings, solve_ms)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?)
            """;

    private static final String INSERT_ITEM = """
            INSERT INTO solver_plan_item (run_id, shipment_id, allocation_status, vehicle_id, priority_code, dock_id,
                slot_id, planned_start_ts, planned_end_ts, sequence_no, earliest_ready_ts, predicted_wait_min,
                booking_action, original_slot_id, pinned, reason_code, detail)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """;

    @Inject
    DataSource dataSource;

    public void save(Api.Run run, String settingsJson) throws SQLException {
        try (Connection c = dataSource.getConnection()) {
            c.setAutoCommit(false);
            try {
                try (PreparedStatement ps = c.prepareStatement(INSERT_RUN)) {
                    Kpis k = run.kpis();
                    int i = 1;
                    ps.setString(i++, run.runId());
                    ps.setString(i++, run.facilityId());
                    ps.setObject(i++, run.asOf());
                    ps.setObject(i++, run.horizonEnd());
                    ps.setString(i++, run.trigger());
                    ps.setString(i++, run.status());
                    ps.setString(i++, run.score());
                    ps.setBoolean(i++, run.feasible());
                    ps.setInt(i++, k.trucksPlanned());
                    ps.setInt(i++, k.assigned());
                    ps.setInt(i++, k.unassigned());
                    ps.setBigDecimal(i++, round(k.averageWaitMin()));
                    ps.setBigDecimal(i++, round(k.priorityWeightedWaitMin()));
                    ps.setInt(i++, k.priorityInversions());
                    ps.setInt(i++, k.pickupInversions());
                    ps.setInt(i++, k.forcedMoves());
                    ps.setInt(i++, k.unforcedMoves());
                    ps.setBigDecimal(i++, round(k.dockUtilisationPct()));
                    ps.setString(i++, settingsJson);
                    ps.setInt(i, (int) Math.min(Integer.MAX_VALUE, run.solveMs()));
                    ps.executeUpdate();
                }
                try (PreparedStatement ps = c.prepareStatement(INSERT_ITEM)) {
                    for (Api.Item it : run.items()) {
                        int i = 1;
                        ps.setString(i++, run.runId());
                        ps.setString(i++, it.shipmentId());
                        ps.setString(i++, it.allocationStatus());
                        ps.setString(i++, it.vehicleId());
                        ps.setString(i++, it.priorityCode());
                        ps.setString(i++, it.dockId());
                        ps.setString(i++, it.slotId());
                        ps.setObject(i++, it.plannedStart());
                        ps.setObject(i++, it.plannedEnd());
                        setInt(ps, i++, it.sequenceNo());
                        ps.setObject(i++, it.earliestReady());
                        setInt(ps, i++, it.predictedWaitMin());
                        ps.setString(i++, it.bookingAction());
                        ps.setString(i++, it.originalSlotId());
                        ps.setBoolean(i++, it.pinned());
                        ps.setString(i++, it.reasonCode());
                        ps.setString(i, it.detail());
                        ps.addBatch();
                    }
                    ps.executeBatch();
                }
                c.commit();
            } catch (SQLException | RuntimeException e) {
                c.rollback();
                throw e;
            }
        }
    }

    public Optional<Api.Run> find(String runId) throws SQLException {
        try (Connection c = dataSource.getConnection()) {
            Api.Run head;
            try (PreparedStatement ps = c.prepareStatement("SELECT * FROM solver_run WHERE run_id = ?")) {
                ps.setString(1, runId);
                try (ResultSet rs = ps.executeQuery()) {
                    if (!rs.next()) {
                        return Optional.empty();
                    }
                    Kpis k = new Kpis(rs.getInt("trucks_planned"), rs.getInt("assigned"), rs.getInt("unassigned"),
                            rs.getDouble("avg_wait_min"), rs.getDouble("weighted_wait_min"),
                            rs.getInt("priority_inversions"), rs.getInt("pickup_inversions"),
                            rs.getInt("forced_moves"), rs.getInt("unforced_moves"), rs.getDouble("dock_utilisation_pct"));
                    head = new Api.Run(rs.getString("run_id"), rs.getString("facility_id"),
                            rs.getObject("as_of_ts", OffsetDateTime.class), rs.getObject("horizon_end_ts", OffsetDateTime.class),
                            rs.getString("run_trigger"), rs.getString("status"), rs.getBoolean("feasible"),
                            rs.getString("score"), rs.getInt("solve_ms"), k, List.of());
                }
            }
            List<Api.Item> items = new ArrayList<>();
            try (PreparedStatement ps = c.prepareStatement("""
                    SELECT * FROM solver_plan_item WHERE run_id = ?
                    ORDER BY (allocation_status <> 'ALLOCATED'), dock_id, planned_start_ts, shipment_id
                    """)) {
                ps.setString(1, runId);
                try (ResultSet rs = ps.executeQuery()) {
                    while (rs.next()) {
                        items.add(new Api.Item(rs.getString("shipment_id"), rs.getString("allocation_status"),
                                rs.getString("vehicle_id"), rs.getString("priority_code"), rs.getString("dock_id"),
                                rs.getString("slot_id"), rs.getObject("planned_start_ts", OffsetDateTime.class),
                                rs.getObject("planned_end_ts", OffsetDateTime.class), (Integer) rs.getObject("sequence_no"),
                                rs.getObject("earliest_ready_ts", OffsetDateTime.class),
                                (Integer) rs.getObject("predicted_wait_min"), rs.getString("booking_action"),
                                rs.getString("original_slot_id"), rs.getBoolean("pinned"), rs.getString("reason_code"),
                                rs.getString("detail")));
                    }
                }
            }
            return Optional.of(new Api.Run(head.runId(), head.facilityId(), head.asOf(), head.horizonEnd(),
                    head.trigger(), head.status(), head.feasible(), head.score(), head.solveMs(), head.kpis(), items));
        }
    }

    private static BigDecimal round(double v) {
        return BigDecimal.valueOf(v).setScale(1, RoundingMode.HALF_UP);
    }

    private static void setInt(PreparedStatement ps, int i, Integer v) throws SQLException {
        if (v == null) {
            ps.setNull(i, Types.INTEGER);
        } else {
            ps.setInt(i, v);
        }
    }
}
