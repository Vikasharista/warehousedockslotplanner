package com.setuhaul.solver.domain;

import ai.timefold.solver.core.api.domain.common.PlanningId;

import java.time.Instant;
import java.util.Objects;

/**
 * One appointment slot on one dock. Times are whole minutes since the Unix epoch.
 *
 * <p>{@code available} is false when the slot is not OPEN, its dock is not ACTIVE, or its window overlaps a
 * dock event (including CAPACITY_REDUCTION). {@code afterLastStart} is true when the slot starts later than the
 * facility's LAST_NEW_START_TIME rule allows.
 */
public final class Slot {

    @PlanningId
    private final String id;
    private final Dock dock;
    private final long startMin;
    private final long endMin;
    private final boolean available;
    private final boolean afterLastStart;

    public Slot(String id, Dock dock, long startMin, long endMin, boolean available, boolean afterLastStart) {
        if (endMin <= startMin) {
            throw new IllegalArgumentException("slot " + id + " ends before it starts");
        }
        this.id = Objects.requireNonNull(id);
        this.dock = Objects.requireNonNull(dock);
        this.startMin = startMin;
        this.endMin = endMin;
        this.available = available;
        this.afterLastStart = afterLastStart;
    }

    public String getId() { return id; }
    public Dock getDock() { return dock; }
    public long getStartMin() { return startMin; }
    public long getEndMin() { return endMin; }
    public long lengthMin() { return endMin - startMin; }
    public boolean isAvailable() { return available; }
    public boolean isAfterLastStart() { return afterLastStart; }
    public Instant startInstant() { return Instant.ofEpochSecond(startMin * 60); }

    @Override
    public boolean equals(Object o) {
        return o instanceof Slot s && s.id.equals(id);
    }

    @Override
    public int hashCode() { return id.hashCode(); }

    @Override
    public String toString() { return id; }
}
