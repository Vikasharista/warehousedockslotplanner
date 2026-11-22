package com.setuhaul.solver.domain;

import ai.timefold.solver.core.api.domain.common.PlanningId;
import ai.timefold.solver.core.api.domain.entity.PlanningEntity;
import ai.timefold.solver.core.api.domain.entity.PlanningPin;
import ai.timefold.solver.core.api.domain.valuerange.ValueRangeProvider;
import ai.timefold.solver.core.api.domain.variable.PlanningVariable;

import java.util.HashSet;
import java.util.List;
import java.util.Set;

/**
 * One truck to unload at the facility. The solver chooses its start slot; it occupies the slot's dock from the
 * slot start for max(unload minutes, slot length).
 */
@PlanningEntity
public class ShipmentAssignment {

    public enum BookingStatus { NONE, PENDING_CONFIRMATION, CONFIRMED }

    @PlanningId
    private String shipmentId;
    private String vehicleId;
    private String priorityCode;
    private int priorityWeight;
    /** STANDARD or HEAVY when the truck needs that dock type; null when any non-reefer dock will do. */
    private Dock.Type requiredDockType;
    private boolean requiresReefer;
    private int loadWeightKg;
    private int unloadMin;
    private long earliestReadyMin;
    private String etaConfidence;
    /** The effective ETA this truck is planned on, epoch minutes; null once it is at the gate. */
    private Long etaMin;
    /** Planned departure of this vehicle's next shipment, in epoch minutes; null when it has none. */
    private Long nextPickupMin;

    /** The truck's current booking, if any, and whether it could be kept (null reason) or not. */
    private Slot originalSlot;
    private String originalSlotId;
    private BookingStatus originalStatus = BookingStatus.NONE;
    private String releaseReason;

    private List<Slot> candidateSlots = List.of();
    private Set<Slot> candidateSet = Set.of();

    @PlanningPin
    private boolean pinned;

    /** A feasible CONFIRMED booking left movable only so that a CRITICAL truck may take its slot. */
    private boolean bumpable;

    @PlanningVariable(valueRangeProviderRefs = "candidateSlots", allowsUnassigned = true)
    private Slot slot;

    public ShipmentAssignment() {
    }

    public ShipmentAssignment(String shipmentId, String priorityCode, int priorityWeight, Dock.Type requiredDockType,
            boolean requiresReefer, int loadWeightKg, int unloadMin, long earliestReadyMin) {
        this.shipmentId = shipmentId;
        this.priorityCode = priorityCode;
        this.priorityWeight = priorityWeight;
        this.requiredDockType = requiredDockType;
        this.requiresReefer = requiresReefer;
        this.loadWeightKg = loadWeightKg;
        this.unloadMin = unloadMin;
        this.earliestReadyMin = earliestReadyMin;
    }

    @ValueRangeProvider(id = "candidateSlots")
    public List<Slot> getCandidateSlots() { return candidateSlots; }

    public void setCandidateSlots(List<Slot> candidateSlots) {
        this.candidateSlots = List.copyOf(candidateSlots);
        this.candidateSet = new HashSet<>(candidateSlots);
    }

    public boolean canUse(Slot s) { return s != null && candidateSet.contains(s); }

    // Derived values used by the constraints. Only meaningful when a slot is assigned.
    public Dock dock() { return slot == null ? null : slot.getDock(); }
    public long startMin() { return slot.getStartMin(); }
    public long endMin() { return slot.getStartMin() + Math.max(unloadMin, slot.lengthMin()); }
    public long waitMin() { return Math.max(0, slot.getStartMin() - earliestReadyMin); }

    /** True when this truck should win a tie against {@code other} under the next-pickup rules. */
    public boolean pickupMoreUrgentThan(ShipmentAssignment other) {
        if (nextPickupMin == null) {
            return false;
        }
        return other.nextPickupMin == null || nextPickupMin < other.nextPickupMin;
    }

    public boolean isConfirmedBooking() { return originalStatus == BookingStatus.CONFIRMED; }
    public boolean wasForcedOffBooking() { return isConfirmedBooking() && releaseReason != null; }
    /** True when the truck had a booking and now sits in a different slot (also when the old slot is in the past). */
    public boolean movedOffOriginal() {
        return originalStatus != BookingStatus.NONE && slot != null && (originalSlot == null || !slot.equals(originalSlot));
    }

    public String getShipmentId() { return shipmentId; }
    public String getVehicleId() { return vehicleId; }
    public void setVehicleId(String vehicleId) { this.vehicleId = vehicleId; }
    public String getPriorityCode() { return priorityCode; }
    public int getPriorityWeight() { return priorityWeight; }
    public Dock.Type requiredDockType() { return requiredDockType; }
    public boolean isRequiresReefer() { return requiresReefer; }
    public int getLoadWeightKg() { return loadWeightKg; }
    public int getUnloadMin() { return unloadMin; }
    public long getEarliestReadyMin() { return earliestReadyMin; }
    public String getEtaConfidence() { return etaConfidence; }
    public void setEtaConfidence(String etaConfidence) { this.etaConfidence = etaConfidence; }
    public Long getEtaMin() { return etaMin; }
    public void setEtaMin(Long etaMin) { this.etaMin = etaMin; }
    public Long getNextPickupMin() { return nextPickupMin; }
    public void setNextPickupMin(Long nextPickupMin) { this.nextPickupMin = nextPickupMin; }
    public Slot getOriginalSlot() { return originalSlot; }
    public String getOriginalSlotId() { return originalSlotId; }
    public BookingStatus getOriginalStatus() { return originalStatus; }
    public String getReleaseReason() { return releaseReason; }

    public void setOriginalBooking(Slot originalSlot, BookingStatus status, String releaseReason) {
        setOriginalBooking(originalSlot == null ? null : originalSlot.getId(), originalSlot, status, releaseReason);
    }

    /** {@code originalSlot} is null when the booked slot lies wholly before the plan's horizon. */
    public void setOriginalBooking(String originalSlotId, Slot originalSlot, BookingStatus status, String releaseReason) {
        this.originalSlotId = originalSlotId;
        this.originalSlot = originalSlot;
        this.originalStatus = status;
        this.releaseReason = releaseReason;
    }

    public boolean isPinned() { return pinned; }
    public boolean isBumpable() { return bumpable; }
    public void setBumpable(boolean bumpable) { this.bumpable = bumpable; }
    /** A bumpable booking that is no longer in its own slot (moved or unassigned). */
    public boolean isBumped() { return bumpable && (slot == null || !slot.equals(originalSlot)); }
    /** A commitment: pinned, or a bumpable booking still in its own slot. */
    public boolean isCommitted() { return pinned || (bumpable && !isBumped()); }
    /**
     * True when this truck's dock time overlaps the dock time {@code bumped} had in its booking: its slot plus any
     * unload running past the slot's end (a long unload also blocks the start of the next slot).
     */
    public boolean takesSlotOf(ShipmentAssignment bumped) {
        Slot o = bumped.originalSlot;
        return slot != null && o != null && slot.getDock().equals(o.getDock())
                && startMin() < o.getStartMin() + Math.max(bumped.unloadMin, o.lengthMin()) && o.getStartMin() < endMin();
    }
    public void setPinned(boolean pinned) { this.pinned = pinned; }
    public Slot getSlot() { return slot; }
    public void setSlot(Slot slot) { this.slot = slot; }

    @Override
    public String toString() { return shipmentId + "@" + slot; }
}
