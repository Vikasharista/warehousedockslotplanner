package solver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplanPostsTheFacility(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/replan", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"busy"}`))
	}))
	defer srv.Close()
	raw, status, err := New(srv.URL+"/", time.Second).Replan(context.Background(), "FAC-JAI-01", "why")
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, status)
	assert.JSONEq(t, `{"error":"busy"}`, string(raw))
	assert.Equal(t, map[string]string{"facilityId": "FAC-JAI-01", "trigger": "MANUAL", "detail": "why"}, got)
}

func TestReplanFailures(t *testing.T) {
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>"))
	}))
	defer html.Close()
	_, _, err := New(html.URL, time.Second).Replan(context.Background(), "F", "")
	assert.Error(t, err)

	_, _, err = New("http://127.0.0.1:1", time.Second).Replan(context.Background(), "F", "")
	assert.Error(t, err)
}
