package api

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	err      error
	lastHold [2]string
	limit    int
}

func (f *fakeStore) Board(_ context.Context, id string) (*store.Board, error) {
	return &store.Board{FacilityID: id}, f.err
}
func (f *fakeStore) Exceptions(context.Context, string) ([]store.Exception, error) {
	return []store.Exception{{ShipmentID: "SHP1"}}, f.err
}
func (f *fakeStore) Changes(_ context.Context, _ string, limit int) ([]store.Change, error) {
	f.limit = limit
	return []store.Change{}, f.err
}
func (f *fakeStore) Runs(_ context.Context, _ string, limit int) ([]store.RunSummary, error) {
	f.limit = limit
	return []store.RunSummary{}, f.err
}
func (f *fakeStore) Options(_ context.Context, _ string, limit int) ([]store.Option, error) {
	f.limit = limit
	return []store.Option{{SlotID: "S1"}}, f.err
}
func (f *fakeStore) CreateHold(_ context.Context, sh, sl string) (*store.Hold, error) {
	f.lastHold = [2]string{sh, sl}
	return &store.Hold{AppointmentID: "H1", ShipmentID: sh, SlotID: sl, Status: "PENDING_CONFIRMATION"}, f.err
}
func (f *fakeStore) GetHold(_ context.Context, id string) (*store.Hold, error) {
	return &store.Hold{AppointmentID: id}, f.err
}
func (f *fakeStore) ConfirmHold(_ context.Context, id string) (*store.Hold, error) {
	return &store.Hold{AppointmentID: id, Status: "CONFIRMED"}, f.err
}
func (f *fakeStore) ReleaseHold(_ context.Context, id string) (*store.Hold, error) {
	return &store.Hold{AppointmentID: id, Status: "CANCELLED"}, f.err
}
func (f *fakeStore) FacilityExists(_ context.Context, id string) (bool, error) {
	return id == "FAC-1", nil
}
func (f *fakeStore) QueueTrucks(_ context.Context, id string, n int) (*store.RequestJob, error) {
	f.limit = n
	return &store.RequestJob{JobID: 7, FacilityID: id, Trucks: n, Status: "PENDING"}, f.err
}
func (f *fakeStore) RequestJob(_ context.Context, id int64) (*store.RequestJob, error) {
	return &store.RequestJob{JobID: id, Status: "DONE"}, f.err
}
func (f *fakeStore) Now(context.Context) (time.Time, error) {
	return time.Date(2026, 8, 4, 4, 25, 0, 0, time.UTC), f.err
}

type fakeSolver struct {
	err    error
	called string
}

func (f *fakeSolver) Replan(_ context.Context, id, _ string) (json.RawMessage, int, error) {
	f.called = id
	return json.RawMessage(`{"status":"COMMITTED"}`), 200, f.err
}

func do(t *testing.T, h http.Handler, method, path, body string) (int, map[string]interface{}, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestReadRoutes(t *testing.T) {
	st, sv := &fakeStore{}, &fakeSolver{}
	h := Handler(st, sv)
	code, body, _ := do(t, h, "GET", "/api/health", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, "UP", body["status"])
	code, body, _ = do(t, h, "GET", "/api/clock", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, "2026-08-04T04:25:00Z", body["now"])
	code, body, _ = do(t, h, "GET", "/api/facilities/FAC-1/board", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, "FAC-1", body["facilityId"])
	code, _, raw := do(t, h, "GET", "/api/facilities/FAC-1/exceptions", "")
	assert.Equal(t, 200, code)
	assert.Contains(t, raw, "SHP1")
	code, _, _ = do(t, h, "GET", "/api/facilities/FAC-1/changes", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, 50, st.limit)
	code, _, _ = do(t, h, "GET", "/api/facilities/FAC-1/runs?limit=5", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, 5, st.limit)
	code, _, _ = do(t, h, "GET", "/api/shipments/SHP1/options", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, 8, st.limit)
}

func TestInputValidation(t *testing.T) {
	h := Handler(&fakeStore{}, &fakeSolver{})
	cases := map[string][2]string{
		"unknown facility":  {"GET", "/api/facilities/FAC-2/board"},
		"bad facility id":   {"GET", "/api/facilities/bad%20id/board"},
		"bad limit":         {"GET", "/api/facilities/FAC-1/runs?limit=0"},
		"limit too high":    {"GET", "/api/facilities/FAC-1/changes?limit=501"},
		"bad shipment id":   {"GET", "/api/shipments/a;b/options"},
		"bad options limit": {"GET", "/api/shipments/SHP1/options?limit=x"},
		"bad hold id":       {"POST", "/api/holds/a;b/confirm"},
		"unknown route":     {"GET", "/api/nope"},
		"wrong method":      {"DELETE", "/api/holds/H1"},
	}
	want := map[string]int{"unknown facility": 404, "unknown route": 404, "wrong method": 405}
	for name, c := range cases {
		code, _, _ := do(t, h, c[0], c[1], "")
		expected := 400
		if w, ok := want[name]; ok {
			expected = w
		}
		assert.Equal(t, expected, code, name)
	}
}

func TestHolds(t *testing.T) {
	st := &fakeStore{}
	h := Handler(st, &fakeSolver{})
	code, body, _ := do(t, h, "POST", "/api/holds", `{"shipmentId":"SHP1","slotId":"S1"}`)
	assert.Equal(t, 201, code)
	assert.Equal(t, "H1", body["appointmentId"])
	assert.Equal(t, [2]string{"SHP1", "S1"}, st.lastHold)

	for _, bad := range []string{``, `{`, `{"shipmentId":"SHP1"}`, `{"shipmentId":"SHP1","slotId":"S1","x":1}`} {
		code, _, _ = do(t, h, "POST", "/api/holds", bad)
		assert.Equal(t, 400, code, bad)
	}
	code, body, _ = do(t, h, "POST", "/api/holds/H1/confirm", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, "CONFIRMED", body["status"])
	code, body, _ = do(t, h, "POST", "/api/holds/H1/release", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, "CANCELLED", body["status"])
	code, _, _ = do(t, h, "GET", "/api/holds/H1", "")
	assert.Equal(t, 200, code)
}

func TestErrorsMapToStatusesAndHideInternals(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
	}{
		"not found":   {errors.NewRecordNotFoundError("no hold H1"), 404},
		"bad request": {errors.NewBadRequestError("nope"), 400},
		"taken":       {errors.New(store.SlotUnavailableCode, "slot S1 is taken"), 409},
		"conflict":    {errors.New(store.HoldConflictCode, "hold is CONFIRMED"), 409},
		"expired":     {errors.New(store.HoldExpiredCode, "expired"), 409},
		"unmapped":    {errors.New("SOMETHING_ELSE", "x"), 500},
		"internal":    {stderrors.New("pq: password authentication failed"), 500},
	}
	for name, c := range cases {
		h := Handler(&fakeStore{err: c.err}, &fakeSolver{})
		code, body, raw := do(t, h, "POST", "/api/holds/H1/confirm", "")
		assert.Equal(t, c.status, code, name)
		if c.status == 500 {
			assert.Equal(t, "internal server error", body["error"], name)
			assert.NotContains(t, raw, "password", name)
		} else {
			assert.Equal(t, c.err.Error(), body["error"], name)
		}
	}
	h := Handler(&fakeStore{err: stderrors.New("db down")}, &fakeSolver{})
	code, _, _ := do(t, h, "GET", "/api/clock", "")
	assert.Equal(t, 500, code)
}

func TestReplanIsProxiedToTheSolver(t *testing.T) {
	sv := &fakeSolver{}
	h := Handler(&fakeStore{}, sv)
	code, body, _ := do(t, h, "POST", "/api/facilities/FAC-1/replan", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, "COMMITTED", body["status"])
	assert.Equal(t, "FAC-1", sv.called)

	h = Handler(&fakeStore{}, &fakeSolver{err: stderrors.New("connection refused")})
	code, body, _ = do(t, h, "POST", "/api/facilities/FAC-1/replan", "")
	assert.Equal(t, 502, code)
	assert.Equal(t, "SOLVER_UNAVAILABLE", body["code"])
	require.NotContains(t, body["error"], "refused")
}

func TestTruckRequests(t *testing.T) {
	st := &fakeStore{}
	h := Handler(st, &fakeSolver{})
	code, body, _ := do(t, h, "POST", "/api/facilities/FAC-1/truck-requests", `{"trucks":10}`)
	assert.Equal(t, 202, code)
	assert.EqualValues(t, 7, body["jobId"])
	assert.Equal(t, 10, st.limit)
	for _, bad := range []string{``, `{"trucks":"x"}`, `{"trucks":5,"extra":1}`} {
		code, _, _ = do(t, h, "POST", "/api/facilities/FAC-1/truck-requests", bad)
		assert.Equal(t, 400, code, bad)
	}
	code, _, _ = do(t, h, "POST", "/api/facilities/FAC-2/truck-requests", `{"trucks":1}`)
	assert.Equal(t, 404, code)
	code, body, _ = do(t, h, "GET", "/api/truck-requests/7", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, "DONE", body["status"])
	code, _, _ = do(t, h, "GET", "/api/truck-requests/x", "")
	assert.Equal(t, 400, code)
}
