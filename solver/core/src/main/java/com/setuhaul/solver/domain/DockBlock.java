package com.setuhaul.solver.domain;

import java.util.Objects;

/**
 * A window in which a dock cannot take a new truck: a dock event, a BLOCKED or CLOSED slot, the hours after the
 * facility closes, a truck still in the dock, or a flagged booking the solver must not move.
 */
public record DockBlock(Dock dock, long startMin, long endMin, Kind kind, String reference) {

    public enum Kind { DOCK_EVENT, BLOCKED_SLOT, FACILITY_CLOSED, TRUCK_IN_DOCK, FLAGGED_BOOKING }

    public DockBlock {
        Objects.requireNonNull(dock);
        Objects.requireNonNull(kind);
        if (endMin <= startMin) {
            throw new IllegalArgumentException("block " + reference + " ends before it starts");
        }
    }

    public boolean overlaps(long fromMin, long toMin) {
        return fromMin < endMin && toMin > startMin;
    }

    public Dock getDock() { return dock; }
    public long getStartMin() { return startMin; }
    public long getEndMin() { return endMin; }
}
