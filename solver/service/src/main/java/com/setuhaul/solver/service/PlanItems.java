package com.setuhaul.solver.service;

import com.setuhaul.solver.domain.DockPlan;
import com.setuhaul.solver.domain.ShipmentAssignment;
import com.setuhaul.solver.domain.ShipmentAssignment.BookingStatus;
import com.setuhaul.solver.load.LoadResult;

import java.time.Instant;
import java.time.OffsetDateTime;
import java.time.ZoneId;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/** Turns a solved plan, plus the trucks the loader left out, into one stored item per truck. */
public final class PlanItems {

    private PlanItems() {
    }

    public static List<Api.Item> of(LoadResult load, DockPlan solved) {
        ZoneId zone = load.zone();
        List<Api.Item> items = new ArrayList<>();

        // Sequence numbers count allocations per dock in start order.
        Map<String, Integer> sequence = new HashMap<>();
        List<ShipmentAssignment> ordered = solved.getAssignments().stream()
                .sorted(Comparator.comparing((ShipmentAssignment a) -> a.getSlot() == null)
                        .thenComparingLong(a -> a.getSlot() == null ? Long.MAX_VALUE : a.startMin())
                        .thenComparing(ShipmentAssignment::getShipmentId))
                .toList();

        for (ShipmentAssignment a : ordered) {
            boolean hadBooking = a.getOriginalStatus() != BookingStatus.NONE;
            OffsetDateTime ready = ts(a.getEarliestReadyMin(), zone);
            OffsetDateTime etaBasis = a.getEtaMin() == null ? null : ts(a.getEtaMin(), zone);
            if (a.getSlot() != null) {
                String dockId = a.getSlot().getDock().getId();
                int seq = sequence.merge(dockId, 1, Integer::sum);
                String action = !hadBooking ? "NEW" : a.movedOffOriginal() ? "MOVED" : "KEPT";
                items.add(new Api.Item(a.getShipmentId(), "ALLOCATED", a.getVehicleId(), a.getPriorityCode(),
                        dockId, a.getSlot().getId(), ts(a.startMin(), zone), ts(a.endMin(), zone), seq, ready,
                        etaBasis, (int) a.waitMin(), action, a.getOriginalSlotId(), a.isPinned(),
                        a.getReleaseReason(), null, null));
            } else {
                String reason = load.emptyCandidateReasons().getOrDefault(a.getShipmentId(), "NO_FREE_SLOT");
                items.add(new Api.Item(a.getShipmentId(), "UNASSIGNED", a.getVehicleId(), a.getPriorityCode(),
                        null, null, null, null, null, ready, etaBasis, null, hadBooking ? "RELEASED" : null,
                        a.getOriginalSlotId(), false, reason,
                        a.getReleaseReason() == null ? null : "booking released: " + a.getReleaseReason(), null));
            }
        }
        notes(items, load.flagged(), "FLAGGED");
        notes(items, load.inDock(), "IN_DOCK");
        notes(items, load.outsideHorizon(), "OUTSIDE_HORIZON");
        return items;
    }

    private static void notes(List<Api.Item> items, List<LoadResult.Note> notes, String status) {
        for (LoadResult.Note n : notes) {
            items.add(new Api.Item(n.shipmentId(), status, null, null, n.dockId(), null, null, null, null, null,
                    null, null, null, n.slotId(), false, n.reason(), n.detail(), null));
        }
    }

    static OffsetDateTime ts(long epochMinutes, ZoneId zone) {
        return Instant.ofEpochSecond(epochMinutes * 60).atZone(zone).toOffsetDateTime();
    }
}
