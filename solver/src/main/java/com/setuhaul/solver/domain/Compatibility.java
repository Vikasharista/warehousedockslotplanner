package com.setuhaul.solver.domain;

/**
 * The per-slot hard rules (H1, H3, H4, H5, H10, H11). The loader uses them to build each truck's candidate slots,
 * and the {@code incompatibleSlot} constraint re-checks them on every assignment, pinned ones included.
 * H2 (same facility) holds by construction: a plan only ever contains one facility's slots.
 */
public final class Compatibility {

    public enum Violation {
        H1_BEFORE_EARLIEST_READY,
        H3_REEFER_DOCK_REQUIRED,
        H4_DOCK_TYPE_MISMATCH,
        H5_SLOT_UNAVAILABLE,
        H10_OVER_DOCK_WEIGHT,
        H11_AFTER_LAST_START
    }

    private Compatibility() {
    }

    /** Rules about the dock alone (no time): reefer, dock type, weight. Null when the dock fits the truck. */
    public static Violation dockViolation(ShipmentAssignment a, Dock dock) {
        if (a.isRequiresReefer() && (dock.getType() != Dock.Type.REEFER || !dock.isSupportsRefrigerated())) {
            return Violation.H3_REEFER_DOCK_REQUIRED;
        }
        Dock.Type needed = a.requiredDockType();
        if (needed != null && needed != dock.getType()) {
            return Violation.H4_DOCK_TYPE_MISMATCH;
        }
        if (a.getLoadWeightKg() > dock.getMaxVehicleWeightKg()) {
            return Violation.H10_OVER_DOCK_WEIGHT;
        }
        return null;
    }

    /** All per-slot rules. Null when the truck may start in this slot. */
    public static Violation violation(ShipmentAssignment a, Slot slot) {
        if (!slot.getDock().isActive() || !slot.isAvailable()) {
            return Violation.H5_SLOT_UNAVAILABLE;
        }
        Violation dock = dockViolation(a, slot.getDock());
        if (dock != null) {
            return dock;
        }
        if (slot.getStartMin() < a.getEarliestReadyMin()) {
            return Violation.H1_BEFORE_EARLIEST_READY;
        }
        if (slot.isAfterLastStart()) {
            return Violation.H11_AFTER_LAST_START;
        }
        return null;
    }
}
