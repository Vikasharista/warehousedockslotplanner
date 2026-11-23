package com.setuhaul.solver;

import com.setuhaul.solver.config.SolverSettings;
import com.setuhaul.solver.domain.DockBlock;
import com.setuhaul.solver.load.LoadResult;
import com.setuhaul.solver.load.PostgresLoader;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.Statement;
import java.time.OffsetDateTime;

import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

/**
 * Phase 7 (C2): a HIGH or CRITICAL truck's slot change waiting for a coordinator keeps its slot reserved until the
 * request expires (approval_reserve_min, 60), not only hold_expiry_min (15) after the hold was made. The solver must
 * keep that slot out of the plan for the request's life. Needs SETUHAUL_TEST_JDBC_URL; writes two rows and removes
 * them again.
 */
class ReservationIT {

    static final String FACILITY = "FAC-JAI-01";
    static final SolverSettings SETTINGS = SolverSettings.defaults();
    static String url;

    @BeforeAll
    static void needsDatabase() {
        url = System.getenv("SETUHAUL_TEST_JDBC_URL");
        assumeTrue(url != null && !url.isBlank(), "SETUHAUL_TEST_JDBC_URL is not set");
    }

    static boolean held(String slotId, String asOf) throws Exception {
        try (Connection c = DriverManager.getConnection(url)) {
            LoadResult load = new PostgresLoader(SETTINGS).load(c, FACILITY, OffsetDateTime.parse(asOf));
            String dock;
            try (PreparedStatement ps = c.prepareStatement("SELECT dock_id FROM appointment_slots WHERE slot_id = ?")) {
                ps.setString(1, slotId);
                try (ResultSet rs = ps.executeQuery()) {
                    rs.next();
                    dock = rs.getString(1);
                }
            }
            return load.plan().getBlocks().stream().anyMatch(b -> b.kind() == DockBlock.Kind.HELD_SLOT
                    && "SHP1009".equals(b.reference()) && b.dock().getId().equals(dock));
        }
    }

    @Test
    void aReservationOutlastsTheHoldExpiryUntilItsRequestExpires() throws Exception {
        String slot;
        try (Connection c = DriverManager.getConnection(url); Statement st = c.createStatement()) {
            try (ResultSet rs = st.executeQuery("""
                    SELECT sl.slot_id FROM appointment_slots sl JOIN docks d ON d.dock_id = sl.dock_id
                    WHERE sl.facility_id = 'FAC-JAI-01' AND sl.slot_status = 'OPEN' AND d.dock_status = 'ACTIVE'
                      AND sl.slot_start_ts >= '2026-08-04T14:00:00+05:30'
                      AND NOT EXISTS (SELECT 1 FROM appointments a WHERE a.slot_id = sl.slot_id
                          AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS'))
                    ORDER BY sl.slot_start_ts LIMIT 1""")) {
                assertThat(rs.next()).isTrue();
                slot = rs.getString(1);
            }
            st.executeUpdate("""
                    INSERT INTO appointments (appointment_id, shipment_id, slot_id, appointment_status, booking_source,
                        is_current, booked_at, updated_at)
                    VALUES ('APT-IT-RESERVE', 'SHP1009', '%s', 'PENDING_CONFIRMATION', 'DRIVER_CHAT', 0,
                            '2026-08-04T09:30:00+05:30', '2026-08-04T09:30:00+05:30')""".formatted(slot));
        }
        try {
            assertThat(held(slot, "2026-08-04T09:40:00+05:30")).as("a live hold blocks its slot").isTrue();
            assertThat(held(slot, "2026-08-04T09:55:00+05:30")).as("15 minutes on, an ordinary hold is gone").isFalse();

            try (Connection c = DriverManager.getConnection(url); Statement st = c.createStatement()) {
                st.executeUpdate("""
                        INSERT INTO slot_change_requests (request_id, facility_id, shipment_id, hold_appointment_id,
                            priority_code, request_status, requested_at, requested_by, expires_at)
                        VALUES ('REQ-IT-RESERVE', 'FAC-JAI-01', 'SHP1009', 'APT-IT-RESERVE', 'CRITICAL', 'PENDING',
                                '2026-08-04T09:31:00+05:30', 'agent:test', '2026-08-04T10:31:00+05:30')""");
            }
            assertThat(held(slot, "2026-08-04T09:55:00+05:30")).as("a reservation keeps its slot").isTrue();
            assertThat(held(slot, "2026-08-04T10:30:00+05:30")).isTrue();
            assertThat(held(slot, "2026-08-04T10:31:00+05:30")).as("until the request expires").isFalse();

            try (Connection c = DriverManager.getConnection(url); Statement st = c.createStatement()) {
                st.executeUpdate("""
                        UPDATE slot_change_requests SET request_status = 'REJECTED',
                            decided_at = '2026-08-04T09:50:00+05:30', decided_by = 'test'
                        WHERE request_id = 'REQ-IT-RESERVE'""");
            }
            assertThat(held(slot, "2026-08-04T09:55:00+05:30")).as("a decided request reserves nothing").isFalse();
        } finally {
            try (Connection c = DriverManager.getConnection(url); Statement st = c.createStatement()) {
                st.executeUpdate("DELETE FROM slot_change_requests WHERE request_id = 'REQ-IT-RESERVE'");
                st.executeUpdate("DELETE FROM appointments WHERE appointment_id = 'APT-IT-RESERVE'");
            }
        }
    }
}
