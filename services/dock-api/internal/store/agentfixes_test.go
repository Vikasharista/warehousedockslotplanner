package store

import (
	"context"
	"testing"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A truck that has reached its dock cannot change its slot by chat (options, hold, confirm); a truck waiting in the
// yard still can.
func TestATruckInTheDockCannotReschedule(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	// SHP1002 is IN_DOCK with its booking IN_PROGRESS.
	_, err := s.Options(ctx, "SHP1002", 5)
	assert.Equal(t, NotReschedulableCode, code(err))
	inDock, err := s.AgentShipment(ctx, "SHP1002")
	require.NoError(t, err)
	assert.False(t, inDock.CanReschedule)
	assert.NotNil(t, inDock.DockIn)

	// SHP1003 is WAITING in the yard: it can still move.
	waiting, err := s.AgentShipment(ctx, "SHP1003")
	require.NoError(t, err)
	assert.True(t, waiting.CanReschedule)
	assert.Nil(t, waiting.DockIn)
	opts, err := s.Options(ctx, "SHP1003", 2)
	require.NoError(t, err)
	require.Len(t, opts, 2)

	_, err = s.CreateHold(ctx, "SHP1002", opts[0].SlotID)
	assert.Equal(t, NotReschedulableCode, code(err), "no hold for a truck in the dock")

	// A hold taken in the yard cannot be confirmed once the truck has entered a dock.
	h, err := s.CreateHold(ctx, "SHP1003", opts[1].SlotID)
	require.NoError(t, err)
	require.NoError(t, s.GetDB(ctx).Exec(`UPDATE facility_checkins SET dock_in_ts = setuhaul_now()
		WHERE shipment_id = 'SHP1003'`).Error)
	_, err = s.ConfirmHold(ctx, h.AppointmentID)
	assert.Equal(t, NotReschedulableCode, code(err))
	after, err := s.AgentShipment(ctx, "SHP1003")
	require.NoError(t, err)
	assert.False(t, after.CanReschedule)
}

// A conversation keeps one open case: a later hand-over (a safety message while a slot case waits) is added to it,
// raising its urgency but never lowering it. Once the case is resolved, the next hand-over opens a new one.
func TestAThreadKeepsOneOpenCase(t *testing.T) {
	s := open(t)
	ctx := agentCtx()
	th, _, err := s.OpenThread(ctx, "SHP1009", "")
	require.NoError(t, err)
	handOver := func(reason, urgency, summary string) *Case {
		t.Helper()
		c, err := s.OpenCase(ctx, NewCase{ReasonCode: reason, Urgency: urgency, Summary: summary, ShipmentID: "SHP1009",
			ThreadID: th.ThreadID, Channel: "TELEGRAM", ChannelUserID: "5"})
		require.NoError(t, err)
		return c
	}

	first := handOver("NO_FEASIBLE_SLOT", "NORMAL", "no slot after 15:00")
	assert.False(t, first.Reused)
	// The agent moved the conversation on while the case stayed open; the next hand-over escalates it again.
	_, err = s.UpdateThread(ctx, th.ThreadID, ThreadUpdate{Status: "RESOLVED"})
	require.NoError(t, err)
	safety := handOver("SAFETY", "URGENT", "accident, helper hurt")
	assert.True(t, safety.Reused)
	escalated, err := getThread(s.GetDB(ctx), th.ThreadID)
	require.NoError(t, err)
	assert.Equal(t, "ESCALATED", escalated.Status)
	assert.Nil(t, escalated.ClosedAt)
	assert.Equal(t, first.CaseID, safety.CaseID)
	assert.Equal(t, "URGENT", safety.Urgency)
	assert.Equal(t, "SAFETY", safety.ReasonCode)
	assert.Equal(t, "no slot after 15:00\nSAFETY: accident, helper hurt", safety.Summary)
	question := handOver("GENERAL_QUESTION", "LOW", "where is the canteen")
	assert.Equal(t, first.CaseID, question.CaseID)
	assert.Equal(t, "URGENT", question.Urgency, "a later hand-over never lowers the urgency")
	assert.Equal(t, "SAFETY", question.ReasonCode)

	cases, err := s.OpenCases(ctx, "FAC-JAI-01")
	require.NoError(t, err)
	onThread := 0
	for _, c := range cases {
		if c.ThreadID != nil && *c.ThreadID == th.ThreadID {
			onThread++
		}
	}
	assert.Equal(t, 1, onThread)

	coordinator := utility.WithUser(context.Background(), "demo@setuhaul.local")
	_, err = s.ResolveCase(coordinator, first.CaseID, "An ambulance is on the way.")
	require.NoError(t, err)
	next := handOver("DRIVER_REQUESTED_HUMAN", "NORMAL", "driver asked for a person")
	assert.False(t, next.Reused)
	assert.NotEqual(t, first.CaseID, next.CaseID)
}
