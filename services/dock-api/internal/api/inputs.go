package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/store"
)

func (h *handlers) registerInputs(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/facilities/{facility}/shipments", h.facility(func(w http.ResponseWriter, r *http.Request, f string) {
		respond(w)(h.st.Shipments(r.Context(), f))
	}))
	mux.HandleFunc("POST /api/facilities/{facility}/shipments", h.facility(h.createShipment))
	mux.HandleFunc("PATCH /api/shipments/{shipment}", h.shipment(h.editShipment))
	mux.HandleFunc("POST /api/shipments/{shipment}/eta", h.shipment(h.shipmentETA))
	mux.HandleFunc("POST /api/shipments/{shipment}/checkin", h.shipment(h.checkIn))
	mux.HandleFunc("GET /api/facilities/{facility}/capacity", h.facility(func(w http.ResponseWriter, r *http.Request, f string) {
		respond(w)(h.st.CapacityView(r.Context(), f))
	}))
	mux.HandleFunc("POST /api/facilities/{facility}/dock-events", h.facility(h.addDockEvent))
	mux.HandleFunc("PATCH /api/facilities/{facility}/docks/{dock}", h.facility(h.setDockStatus))
	mux.HandleFunc("POST /api/slots/{slot}/status", h.setSlotStatus)
}

// createShipment adds a truck request made now; the solver's NEW_REQUEST trigger plans it.
func (h *handlers) createShipment(w http.ResponseWriter, r *http.Request, facility string) {
	var req store.NewShipment
	if !decode(w, r, &req, "a new shipment") {
		return
	}
	row, err := h.st.CreateShipment(r.Context(), facility, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (h *handlers) editShipment(w http.ResponseWriter, r *http.Request, id string) {
	var req store.ShipmentEdit
	if !decode(w, r, &req, `{"priorityCode"?, "requiredDockType"?, "loadWeightKg"?, "unloadMin"?}`) {
		return
	}
	facility, err := h.st.UpdateShipment(r.Context(), id, req)
	if err != nil {
		writeError(w, err)
		return
	}
	h.replanSoon(facility, "shipment "+id+" edited")
	writeJSON(w, http.StatusOK, map[string]string{"shipmentId": id, "status": "UPDATED"})
}

// shipmentETA records a driver-declared ETA or an operations override; an override also re-plans at once.
func (h *handlers) shipmentETA(w http.ResponseWriter, r *http.Request, id string) {
	var req etaRequest
	if !decode(w, r, &req, `{"eta", "source", "confidence"?, "reason"?, "note"?}`) {
		return
	}
	req.Source = strings.ToUpper(req.Source)
	facility, err := h.st.ReportETA(r.Context(), id, req.Eta, req.Source, strings.ToUpper(req.Confidence),
		strings.ToUpper(req.Reason), req.Note)
	if err != nil {
		writeError(w, err)
		return
	}
	sh, err := h.st.AgentShipment(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if req.Source == store.SourceOperations {
		h.replanSoon(facility, "ETA override for "+id)
	}
	writeJSON(w, http.StatusOK, sh)
}

// checkIn puts a truck at the gate now; the solver's CHECKIN trigger re-plans.
func (h *handlers) checkIn(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.st.CheckIn(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	respond(w)(h.st.AgentShipment(r.Context(), id))
}

func (h *handlers) addDockEvent(w http.ResponseWriter, r *http.Request, facility string) {
	var req store.NewDockEvent
	if !decode(w, r, &req, `{"dockId", "type", "start", "end"?, "reason"}`) {
		return
	}
	code, err := h.st.AddDockEvent(r.Context(), facility, req)
	if err != nil {
		writeError(w, err)
		return
	}
	h.replanSoon(facility, fmt.Sprintf("dock event: %s %s", code, strings.ToLower(req.Type)))
	writeJSON(w, http.StatusCreated, map[string]string{"dockCode": code, "status": "ADDED"})
}

func (h *handlers) setDockStatus(w http.ResponseWriter, r *http.Request, facility string) {
	dock := r.PathValue("dock")
	if !idPattern.MatchString(dock) {
		writeError(w, errors.NewBadRequestError("invalid dock id"))
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &req, `{"status": "ACTIVE" | "MAINTENANCE" | "OUT_OF_SERVICE"}`) {
		return
	}
	code, err := h.st.SetDockStatus(r.Context(), facility, dock, req.Status)
	if err != nil {
		writeError(w, err)
		return
	}
	h.replanSoon(facility, fmt.Sprintf("dock %s set to %s", code, strings.ToUpper(req.Status)))
	writeJSON(w, http.StatusOK, map[string]string{"dockCode": code, "status": strings.ToUpper(req.Status)})
}

func (h *handlers) setSlotStatus(w http.ResponseWriter, r *http.Request) {
	slot := r.PathValue("slot")
	if !idPattern.MatchString(slot) {
		writeError(w, errors.NewBadRequestError("invalid slot id"))
		return
	}
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &req, `{"status": "OPEN" | "BLOCKED", "reason"?}`) {
		return
	}
	facility, label, err := h.st.SetSlotStatus(r.Context(), slot, req.Status, req.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	h.replanSoon(facility, fmt.Sprintf("slot %s %s", label, strings.ToLower(req.Status)))
	writeJSON(w, http.StatusOK, map[string]string{"slotId": slot, "status": strings.ToUpper(req.Status)})
}
