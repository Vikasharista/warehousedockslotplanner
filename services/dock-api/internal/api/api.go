// Package api is the HTTP API the plan view (and later the driver agent) calls. All routes are under /api.
package api

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/logger"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/store"
	"go.uber.org/zap"
)

// Store is what the handlers need from the database.
type Store interface {
	Board(ctx context.Context, facilityID string) (*store.Board, error)
	Exceptions(ctx context.Context, facilityID string) ([]store.Exception, error)
	Changes(ctx context.Context, facilityID string, limit int) ([]store.Change, error)
	Runs(ctx context.Context, facilityID string, limit int) ([]store.RunSummary, error)
	Options(ctx context.Context, shipmentID string, limit int) ([]store.Option, error)
	CreateHold(ctx context.Context, shipmentID, slotID string) (*store.Hold, error)
	GetHold(ctx context.Context, id string) (*store.Hold, error)
	ConfirmHold(ctx context.Context, id string) (*store.Hold, error)
	ReleaseHold(ctx context.Context, id string) (*store.Hold, error)
	FacilityExists(ctx context.Context, facilityID string) (bool, error)
	QueueTrucks(ctx context.Context, facilityID string, n int) (*store.RequestJob, error)
	RequestJob(ctx context.Context, id int64) (*store.RequestJob, error)
	Now(ctx context.Context) (time.Time, error)
}

// Solver is what the handlers need from the solver service.
type Solver interface {
	Replan(ctx context.Context, facilityID, detail string) (json.RawMessage, int, error)
}

var statusByCode = map[string]int{
	errors.BadRequestErrorCode:     http.StatusBadRequest,
	errors.RecordNotFoundErrorCode: http.StatusNotFound,
	store.SlotUnavailableCode:      http.StatusConflict,
	store.HoldConflictCode:         http.StatusConflict,
	store.HoldExpiredCode:          http.StatusConflict,
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Handler returns the API's routes.
func Handler(st Store, sv Solver) http.Handler {
	h := &handlers{st: st, sv: sv}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
	})
	mux.HandleFunc("GET /api/clock", h.clock)
	mux.HandleFunc("GET /api/facilities/{facility}/board", h.facility(h.board))
	mux.HandleFunc("GET /api/facilities/{facility}/exceptions", h.facility(h.exceptions))
	mux.HandleFunc("GET /api/facilities/{facility}/changes", h.facility(h.changes))
	mux.HandleFunc("GET /api/facilities/{facility}/runs", h.facility(h.runs))
	mux.HandleFunc("POST /api/facilities/{facility}/replan", h.facility(h.replan))
	mux.HandleFunc("POST /api/facilities/{facility}/truck-requests", h.facility(h.queueTrucks))
	mux.HandleFunc("GET /api/truck-requests/{job}", h.requestJob)
	mux.HandleFunc("GET /api/shipments/{shipment}/options", h.options)
	mux.HandleFunc("POST /api/holds", h.createHold)
	mux.HandleFunc("GET /api/holds/{hold}", h.hold(h.st.GetHold))
	mux.HandleFunc("POST /api/holds/{hold}/confirm", h.hold(h.st.ConfirmHold))
	mux.HandleFunc("POST /api/holds/{hold}/release", h.hold(h.st.ReleaseHold))
	return mux
}

type handlers struct {
	st Store
	sv Solver
}

func (h *handlers) clock(w http.ResponseWriter, r *http.Request) {
	now, err := h.st.Now(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"now": now})
}

// facility validates the {facility} path value and that it exists before calling next.
func (h *handlers) facility(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("facility")
		if !idPattern.MatchString(id) {
			writeError(w, errors.NewBadRequestError("invalid facility id"))
			return
		}
		ok, err := h.st.FacilityExists(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		if !ok {
			writeError(w, errors.NewRecordNotFoundError("no facility "+id))
			return
		}
		next(w, r, id)
	}
}

func (h *handlers) board(w http.ResponseWriter, r *http.Request, facility string) {
	respond(w)(h.st.Board(r.Context(), facility))
}

func (h *handlers) exceptions(w http.ResponseWriter, r *http.Request, facility string) {
	respond(w)(h.st.Exceptions(r.Context(), facility))
}

func (h *handlers) changes(w http.ResponseWriter, r *http.Request, facility string) {
	limit, err := limitParam(r, 50, 500)
	if err != nil {
		writeError(w, err)
		return
	}
	respond(w)(h.st.Changes(r.Context(), facility, limit))
}

func (h *handlers) runs(w http.ResponseWriter, r *http.Request, facility string) {
	limit, err := limitParam(r, 20, 200)
	if err != nil {
		writeError(w, err)
		return
	}
	respond(w)(h.st.Runs(r.Context(), facility, limit))
}

func (h *handlers) replan(w http.ResponseWriter, r *http.Request, facility string) {
	raw, status, err := h.sv.Replan(r.Context(), facility, "requested from the plan view")
	if err != nil {
		logger.GetLogger().With(zap.Error(err)).Error("api.replan")
		writeJSON(w, http.StatusBadGateway, map[string]string{"code": "SOLVER_UNAVAILABLE", "error": "the solver service did not answer"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// queueTrucks asks for generated truck requests: {"trucks": n}, 1 to 50. 202 with the queued job.
func (h *handlers) queueTrucks(w http.ResponseWriter, r *http.Request, facility string) {
	var req struct {
		Trucks int `json:"trucks"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, errors.NewBadRequestError("body must be {\"trucks\": n}"))
		return
	}
	job, err := h.st.QueueTrucks(r.Context(), facility, req.Trucks)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (h *handlers) requestJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("job"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, errors.NewBadRequestError("invalid job id"))
		return
	}
	respond(w)(h.st.RequestJob(r.Context(), id))
}

func (h *handlers) options(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("shipment")
	if !idPattern.MatchString(id) {
		writeError(w, errors.NewBadRequestError("invalid shipment id"))
		return
	}
	limit, err := limitParam(r, 8, 50)
	if err != nil {
		writeError(w, err)
		return
	}
	respond(w)(h.st.Options(r.Context(), id, limit))
}

type createHoldRequest struct {
	ShipmentID string `json:"shipmentId"`
	SlotID     string `json:"slotId"`
}

func (h *handlers) createHold(w http.ResponseWriter, r *http.Request) {
	var req createHoldRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, errors.NewBadRequestError("body must be {\"shipmentId\": ..., \"slotId\": ...}"))
		return
	}
	if !idPattern.MatchString(req.ShipmentID) || !idPattern.MatchString(req.SlotID) {
		writeError(w, errors.NewBadRequestError("shipmentId and slotId are required"))
		return
	}
	hold, err := h.st.CreateHold(r.Context(), req.ShipmentID, req.SlotID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, hold)
}

func (h *handlers) hold(op func(context.Context, string) (*store.Hold, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("hold")
		if !idPattern.MatchString(id) {
			writeError(w, errors.NewBadRequestError("invalid hold id"))
			return
		}
		respond(w)(op(r.Context(), id))
	}
}

func limitParam(r *http.Request, def, max int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > max {
		return 0, errors.NewBadRequestError("limit must be between 1 and " + strconv.Itoa(max))
	}
	return n, nil
}

func respond(w http.ResponseWriter) func(interface{}, error) {
	return func(v interface{}, err error) {
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers coded errors with their status and message; anything else is logged and hidden.
func writeError(w http.ResponseWriter, err error) {
	var base errors.BaseErr
	if stderrors.As(err, &base) {
		if status, ok := statusByCode[base.Code()]; ok {
			writeJSON(w, status, map[string]string{"code": base.Code(), "error": base.Error()})
			return
		}
	}
	logger.GetLogger().With(zap.Error(err)).Error("api.InternalError")
	writeJSON(w, http.StatusInternalServerError, map[string]string{"code": errors.InternalServerErrorCode,
		"error": "internal server error"})
}
