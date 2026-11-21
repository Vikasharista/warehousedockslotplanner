package com.setuhaul.solver.domain;

import ai.timefold.solver.core.api.domain.common.PlanningId;

import java.util.Objects;

/** A physical dock door. Only ACTIVE docks offer slots to the solver. */
public final class Dock {

    public enum Type { STANDARD, REEFER, HEAVY }

    @PlanningId
    private final String id;
    private final String code;
    private final Type type;
    private final boolean supportsRefrigerated;
    private final int maxVehicleWeightKg;
    private final boolean active;

    public Dock(String id, String code, Type type, boolean supportsRefrigerated, int maxVehicleWeightKg, boolean active) {
        this.id = Objects.requireNonNull(id);
        this.code = code;
        this.type = Objects.requireNonNull(type);
        this.supportsRefrigerated = supportsRefrigerated;
        this.maxVehicleWeightKg = maxVehicleWeightKg;
        this.active = active;
    }

    public String getId() { return id; }
    public String getCode() { return code; }
    public Type getType() { return type; }
    public boolean isSupportsRefrigerated() { return supportsRefrigerated; }
    public int getMaxVehicleWeightKg() { return maxVehicleWeightKg; }
    public boolean isActive() { return active; }

    @Override
    public boolean equals(Object o) {
        return o instanceof Dock d && d.id.equals(id);
    }

    @Override
    public int hashCode() { return id.hashCode(); }

    @Override
    public String toString() { return code; }
}
