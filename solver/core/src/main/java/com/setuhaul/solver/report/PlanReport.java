package com.setuhaul.solver.report;

import com.setuhaul.solver.domain.Dock;
import com.setuhaul.solver.domain.DockBlock;
import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.ShipmentAssignment;
import com.setuhaul.solver.load.LoadResult;

import java.time.Instant;
import java.time.ZoneId;
import java.time.format.DateTimeFormatter;
import java.util.Comparator;
import java.util.List;
import java.util.Locale;

/** Renders a solved plan as plain text: dock sequence, trucks left out, and the KPIs. */
public final class PlanReport {

    private static final DateTimeFormatter HM = DateTimeFormatter.ofPattern("HH:mm");
    private static final DateTimeFormatter DHM = DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm");

    private PlanReport() {
    }

    public static String render(LoadResult load, DockPlan solved) {
        ZoneId zone = load.zone();
        StringBuilder out = new StringBuilder();
        out.append("Dock plan for ").append(solved.getFacilityId())
                .append(", as of ").append(DHM.format(load.asOf().atZone(zone)))
                .append(" (horizon to ").append(DHM.format(load.horizonEnd().atZone(zone))).append(", ").append(zone).append(")\n");
        out.append("Score: ").append(solved.getScore())
                .append("   [hard | L1 unassigned | L2 priority inversions | L3 pickup inversions | L4 weighted wait + moves]\n");
        out.append("Feasible: ").append(solved.getScore() != null && solved.getScore().isFeasible() ? "yes" : "NO").append("\n\n");

        out.append("DOCK SEQUENCE\n");
        for (Dock dock : solved.getDocks()) {
            List<String> rows = new java.util.ArrayList<>();
            solved.getBlocks().stream()
                    .filter(b -> b.dock().equals(dock) && b.kind() == DockBlock.Kind.TRUCK_IN_DOCK
                            || b.dock().equals(dock) && b.kind() == DockBlock.Kind.FLAGGED_BOOKING
                            || b.dock().equals(dock) && b.kind() == DockBlock.Kind.HELD_SLOT)
                    .sorted(Comparator.comparingLong(DockBlock::startMin))
                    .forEach(b -> rows.add(String.format(Locale.ROOT, "  %s-%s  %-8s  %s",
                            t(b.startMin(), zone), t(b.endMin(), zone), b.reference(),
                            b.kind() == DockBlock.Kind.TRUCK_IN_DOCK ? "in dock (not planned)"
                            : b.kind() == DockBlock.Kind.HELD_SLOT ? "held for a driver" : "flagged booking, left as is")));
            solved.getAssignments().stream()
                    .filter(a -> a.getSlot() != null && a.dock().equals(dock))
                    .sorted(Comparator.comparingLong(ShipmentAssignment::startMin))
                    .forEach(a -> rows.add(String.format(Locale.ROOT, "  %s-%s  %-8s  %-8s  wait %3d min  %s",
                            t(a.startMin(), zone), t(a.endMin(), zone), a.getShipmentId(), a.getPriorityCode(),
                            a.waitMin(), status(a))));
            out.append(String.format(Locale.ROOT, "%s (%s, max %,d kg)%s\n", dock.getCode(), dock.getType(),
                    dock.getMaxVehicleWeightKg(), dock.isActive() ? "" : " INACTIVE"));
            if (rows.isEmpty()) {
                out.append("  (nothing planned)\n");
            } else {
                rows.stream().sorted().forEach(r -> out.append(r).append('\n'));
            }
        }

        out.append("\nUNASSIGNED (to a coordinator)\n");
        List<ShipmentAssignment> unassigned = solved.getAssignments().stream().filter(a -> a.getSlot() == null).toList();
        if (unassigned.isEmpty()) {
            out.append("  none\n");
        }
        for (ShipmentAssignment a : unassigned) {
            String reason = load.emptyCandidateReasons().getOrDefault(a.getShipmentId(), "NO_FREE_SLOT");
            out.append(String.format(Locale.ROOT, "  %-8s  %-8s  ready %s  %s%s\n", a.getShipmentId(), a.getPriorityCode(),
                    t(a.getEarliestReadyMin(), zone), reason,
                    a.getReleaseReason() == null ? "" : " (booking released: " + a.getReleaseReason() + ")"));
        }
        notes(out, "FLAGGED (not moved automatically)", load.flagged());
        notes(out, "IN DOCK", load.inDock());
        notes(out, "OUTSIDE HORIZON", load.outsideHorizon());

        long from = load.asOf().getEpochSecond() / 60;
        long to = load.horizonEnd().getEpochSecond() / 60;
        Kpis k = Kpis.of(solved, from, to);
        out.append("\nKPIs\n");
        out.append(String.format(Locale.ROOT, "  trucks planned            %d (assigned %d, unassigned %d)%n",
                k.trucksPlanned(), k.assigned(), k.unassigned()));
        out.append(String.format(Locale.ROOT, "  average wait              %.1f min%n", k.averageWaitMin()));
        out.append(String.format(Locale.ROOT, "  priority-weighted wait    %.1f min%n", k.priorityWeightedWaitMin()));
        out.append(String.format(Locale.ROOT, "  priority inversions       %d%n", k.priorityInversions()));
        out.append(String.format(Locale.ROOT, "  next-pickup inversions    %d%n", k.pickupInversions()));
        out.append(String.format(Locale.ROOT, "  bookings moved            %d forced, %d unforced%n", k.forcedMoves(), k.unforcedMoves()));
        out.append(String.format(Locale.ROOT, "  dock utilisation          %.1f %%%n", k.dockUtilisationPct()));
        out.append("\nDry run: nothing was written to the database.\n");
        return out.toString();
    }

    private static String status(ShipmentAssignment a) {
        if (a.isPinned()) {
            return a.getOriginalStatus() == ShipmentAssignment.BookingStatus.PENDING_CONFIRMATION
                    ? "driver hold kept" : "booking kept";
        }
        if (a.getOriginalStatus() == ShipmentAssignment.BookingStatus.NONE) {
            return "new allocation";
        }
        if (!a.movedOffOriginal()) {
            return "booking kept (not pinned)";
        }
        return "re-planned from " + a.getOriginalSlotId()
                + (a.getReleaseReason() == null ? "" : " (" + a.getReleaseReason() + ")");
    }

    private static void notes(StringBuilder out, String title, List<LoadResult.Note> notes) {
        out.append('\n').append(title).append('\n');
        if (notes.isEmpty()) {
            out.append("  none\n");
        }
        for (LoadResult.Note n : notes) {
            out.append(String.format(Locale.ROOT, "  %-8s  %-20s  %s%n", n.shipmentId(), n.reason(), n.detail()));
        }
    }

    private static String t(long epochMinutes, ZoneId zone) {
        return HM.format(Instant.ofEpochSecond(epochMinutes * 60).atZone(zone));
    }
}
