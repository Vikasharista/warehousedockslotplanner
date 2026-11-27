package api

import (
	"net/http"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/store"
)

// Phase 7: the agent's conversation records (agent key or session) and the coordinator's Driver cases screen
// (session only: an agent key opens nothing outside /api/agent/).

func (h *handlers) registerCases(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agent/threads", h.openThread)
	mux.HandleFunc("PATCH /api/agent/threads/{thread}", h.id("thread", func(w http.ResponseWriter, r *http.Request, id string) {
		var req store.ThreadUpdate
		if !decode(w, r, &req, `{"status"?, "intent"?}`) {
			return
		}
		respond(w)(h.st.UpdateThread(r.Context(), id, req))
	}))
	mux.HandleFunc("POST /api/agent/threads/{thread}/messages", h.id("thread", h.logMessage))
	mux.HandleFunc("POST /api/agent/exceptions", h.reportException)
	mux.HandleFunc("PATCH /api/agent/exceptions/{exception}", h.id("exception", func(w http.ResponseWriter, r *http.Request, id string) {
		var req struct {
			Status string `json:"status"`
		}
		if !decode(w, r, &req, `{"status": "..."}`) {
			return
		}
		respond(w)(h.st.SetExceptionStatus(r.Context(), id, req.Status))
	}))
	mux.HandleFunc("POST /api/agent/cases", h.openCase)
	mux.HandleFunc("GET /api/agent/cases/{case}", h.id("case", func(w http.ResponseWriter, r *http.Request, id string) {
		respond(w)(h.st.GetCase(r.Context(), id))
	}))
	mux.HandleFunc("GET /api/agent/facilities/{facility}", h.id("facility", func(w http.ResponseWriter, r *http.Request, id string) {
		respond(w)(h.st.FacilityInfo(r.Context(), id))
	}))

	mux.HandleFunc("GET /api/facilities/{facility}/driver-cases", h.facility(func(w http.ResponseWriter, r *http.Request, f string) {
		respond(w)(h.st.DriverCasesView(r.Context(), f))
	}))
	mux.HandleFunc("GET /api/threads/{thread}/messages", h.id("thread", func(w http.ResponseWriter, r *http.Request, id string) {
		respond(w)(h.st.ThreadMessages(r.Context(), id))
	}))
	mux.HandleFunc("POST /api/approvals/{request}/approve", h.id("request", h.decide(true)))
	mux.HandleFunc("POST /api/approvals/{request}/reject", h.id("request", h.decide(false)))
	mux.HandleFunc("POST /api/cases/{case}/assign", h.id("case", func(w http.ResponseWriter, r *http.Request, id string) {
		respond(w)(h.st.AssignCase(r.Context(), id))
	}))
	mux.HandleFunc("POST /api/cases/{case}/resolve", h.id("case", func(w http.ResponseWriter, r *http.Request, id string) {
		var req struct {
			Reply string `json:"reply"`
		}
		if !decodeOptional(w, r, &req, `{"reply"?: "text for the driver"}`) {
			return
		}
		respond(w)(h.st.ResolveCase(r.Context(), id, req.Reply))
	}))
}

// id validates the named path value before calling next.
func (h *handlers) id(name string, next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := r.PathValue(name)
		if !idPattern.MatchString(v) {
			writeError(w, errors.NewBadRequestError("invalid "+name+" id"))
			return
		}
		next(w, r, v)
	}
}

// openThread returns the shipment's open conversation (200) or opens one (201).
func (h *handlers) openThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShipmentID string `json:"shipmentId"`
		Intent     string `json:"intent"`
	}
	if !decode(w, r, &req, `{"shipmentId": "...", "intent"?: "..."}`) {
		return
	}
	if !idPattern.MatchString(req.ShipmentID) {
		writeError(w, errors.NewBadRequestError("shipmentId is required"))
		return
	}
	t, created, err := h.st.OpenThread(r.Context(), req.ShipmentID, req.Intent)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[created], t)
}

// logMessage stores a message: 201, or 200 with "duplicate": true when the external id was already logged.
func (h *handlers) logMessage(w http.ResponseWriter, r *http.Request, thread string) {
	var req store.NewMessage
	if !decode(w, r, &req, `{"senderType", "text", "externalMessageId"?, "parsedIntent"?, "extractedEta"?, "requiresHumanReview"?}`) {
		return
	}
	m, err := h.st.LogMessage(r.Context(), thread, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[bool]int{true: http.StatusOK, false: http.StatusCreated}[m.Duplicate], m)
}

// reportException records (201) or updates (200) the thread's live exception.
func (h *handlers) reportException(w http.ResponseWriter, r *http.Request) {
	var req store.ExceptionReport
	if !decode(w, r, &req, `{"threadId", "type", "description", "delayMin"?, "declaredEta"?, "earliestAcceptable"?, "latestAcceptable"?, "status"?}`) {
		return
	}
	if !idPattern.MatchString(req.ThreadID) {
		writeError(w, errors.NewBadRequestError("threadId is required"))
		return
	}
	x, created, err := h.st.ReportException(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[created], x)
}

func (h *handlers) openCase(w http.ResponseWriter, r *http.Request) {
	var req store.NewCase
	if !decode(w, r, &req, `{"reasonCode", "urgency", "summary", "channel", "shipmentId"?, "threadId"?, "exceptionId"?, "channelUserId"?, "phone"?}`) {
		return
	}
	for _, v := range []string{req.ShipmentID, req.ThreadID, req.ExceptionID} {
		if v != "" && !idPattern.MatchString(v) {
			writeError(w, errors.NewBadRequestError("invalid shipmentId, threadId or exceptionId"))
			return
		}
	}
	c, err := h.st.OpenCase(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	// 200 when the hand-over was added to the thread's open case, 201 for a new case.
	writeJSON(w, map[bool]int{true: http.StatusOK, false: http.StatusCreated}[c.Reused], c)
}

func (h *handlers) decide(approve bool) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		var req struct {
			Note string `json:"note"`
		}
		if !decodeOptional(w, r, &req, `{"note"?: "..."}`) {
			return
		}
		out, err := h.st.DecideRequest(r.Context(), id, approve, req.Note)
		if err != nil {
			writeError(w, err)
			return
		}
		h.replanSoon(out.FacilityID, "slot change "+id+" "+out.Status)
		writeJSON(w, http.StatusOK, out)
	}
}

// confirmHold answers 200 when the hold became the booking and 202 when it waits for a coordinator's approval.
func (h *handlers) confirmHold(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("hold")
	if !idPattern.MatchString(id) {
		writeError(w, errors.NewBadRequestError("invalid hold id"))
		return
	}
	hold, err := h.st.ConfirmHold(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if hold.Outcome == store.OutcomeAwaitingApproval {
		status = http.StatusAccepted
	}
	writeJSON(w, status, hold)
}

// decodeOptional is decode for a body that may be left out entirely.
func decodeOptional(w http.ResponseWriter, r *http.Request, v interface{}, shape string) bool {
	if r.ContentLength == 0 {
		return true
	}
	return decode(w, r, v, shape)
}
