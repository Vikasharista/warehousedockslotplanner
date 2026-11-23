package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/logger"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/store"
	"go.uber.org/zap"
)

// AgentKeys are the API keys driver agents send in X-Agent-Key, by agent name.
type AgentKeys map[string]string

// ParseAgentKeys reads "name:key" entries separated by commas; keys are at least 32 characters.
func ParseAgentKeys(raw string) (AgentKeys, error) {
	keys := AgentKeys{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, key, ok := strings.Cut(entry, ":")
		if !ok || name == "" {
			return nil, fmt.Errorf("agent key entries must be name:key")
		}
		if len(key) < 32 {
			return nil, fmt.Errorf("the agent key for %s must be at least 32 characters", name)
		}
		keys[name] = key
	}
	return keys, nil
}

// match returns the agent whose key this is, comparing in constant time.
func (k AgentKeys) match(key string) (string, bool) {
	found := ""
	for name, want := range k {
		if subtle.ConstantTimeCompare([]byte(key), []byte(want)) == 1 {
			found = name
		}
	}
	return found, found != ""
}

func (h *handlers) registerAgent(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agent/drivers/{phone}/shipments", func(w http.ResponseWriter, r *http.Request) {
		respond(w)(h.st.ShipmentsByPhone(r.Context(), r.PathValue("phone")))
	})
	mux.HandleFunc("GET /api/agent/shipments/{shipment}", h.shipment(func(w http.ResponseWriter, r *http.Request, id string) {
		respond(w)(h.st.AgentShipment(r.Context(), id))
	}))
	mux.HandleFunc("GET /api/agent/shipments/{shipment}/options", h.options)
	mux.HandleFunc("POST /api/agent/shipments/{shipment}/eta", h.shipment(h.agentETA))
	mux.HandleFunc("POST /api/agent/holds", h.createHold)
	mux.HandleFunc("POST /api/agent/holds/{hold}/confirm", h.confirmHold)
	mux.HandleFunc("POST /api/agent/holds/{hold}/release", h.hold(h.st.ReleaseHold))
	mux.HandleFunc("GET /api/agent/changes", h.changesFeed)
}

// shipment validates the {shipment} path value before calling next.
func (h *handlers) shipment(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("shipment")
		if !idPattern.MatchString(id) {
			writeError(w, errors.NewBadRequestError("invalid shipment id"))
			return
		}
		next(w, r, id)
	}
}

type etaRequest struct {
	Eta        time.Time `json:"eta"`
	Source     string    `json:"source"`
	Confidence string    `json:"confidence"`
	Reason     string    `json:"reason"`
	Note       string    `json:"note"`
}

func decode(w http.ResponseWriter, r *http.Request, v interface{}, shape string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, errors.NewBadRequestError("body must be "+shape))
		return false
	}
	return true
}

// agentETA records a driver's reported ETA and returns the truck's updated status. The solver's ETA trigger
// re-plans if it moved more than its threshold from the ETA the slot was planned on.
func (h *handlers) agentETA(w http.ResponseWriter, r *http.Request, id string) {
	var req etaRequest
	if !decode(w, r, &req, `{"eta": "<RFC 3339 time>", "reason"?: "TRAFFIC", "note"?: "..."}`) {
		return
	}
	if _, err := h.st.ReportETA(r.Context(), id, req.Eta, store.SourceDriver, req.Confidence, req.Reason, req.Note); err != nil {
		writeError(w, err)
		return
	}
	respond(w)(h.st.AgentShipment(r.Context(), id))
}

// changesFeed returns booking changes after a cursor: {"changes": [...], "next": <cursor to send next time>,
// "last": <newest change id>}. A cursor above "last" means the feed was reset (nightly demo reset): start again at 0.
func (h *handlers) changesFeed(w http.ResponseWriter, r *http.Request) {
	after := int64(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, errors.NewBadRequestError("after must be a change id (0 or more)"))
			return
		}
		after = n
	}
	limit, err := limitParam(r, 100, 500)
	if err != nil {
		writeError(w, err)
		return
	}
	changes, last, err := h.st.ChangesAfter(r.Context(), after, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	next := after
	if len(changes) > 0 {
		next = changes[len(changes)-1].ChangeID
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"changes": changes, "next": next, "last": last})
}

// agentOrSession lets a request with a valid X-Agent-Key through on /api/agent routes, as "agent:<name>".
func agentOrSession(keys AgentKeys, au Auth, r *http.Request) (context.Context, error) {
	if key := r.Header.Get("X-Agent-Key"); key != "" && strings.HasPrefix(r.URL.Path, "/api/agent/") {
		if name, ok := keys.match(key); ok {
			return utility.WithUser(r.Context(), "agent:"+name), nil
		}
		return nil, errors.NewUnauthenticatedError("unknown agent key")
	}
	return au.Authenticate(r)
}

// replanSoon asks the solver to re-plan in the background after a change the triggers do not watch.
func (h *handlers) replanSoon(facility, detail string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if _, status, err := h.sv.Replan(ctx, facility, detail); err != nil || status >= 500 {
			logger.GetLogger().With(zap.Error(err), zap.Int("status", status)).Warn("api.replanSoon")
		}
	}()
}
