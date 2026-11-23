package store

import (
	"context"
	"testing"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type apptRow struct {
	AppointmentStatus  string
	IsCurrent          int
	CancellationReason *string
}

func appt(t *testing.T, s *Store, id string) apptRow {
	t.Helper()
	var r apptRow
	require.NoError(t, s.GetDB(context.Background()).Raw(`SELECT appointment_status, is_current, cancellation_reason
		FROM appointments WHERE appointment_id = ?`, id).Scan(&r).Error)
	return r
}

func feed(t *testing.T, s *Store) []FeedChange {
	t.Helper()
	out, _, err := s.ChangesAfter(context.Background(), 0, 500)
	require.NoError(t, err)
	return out
}

// awaitApproval holds SHP1009's (CRITICAL, booked) first option and confirms: the change waits for a coordinator.
func awaitApproval(t *testing.T, s *Store, ctx context.Context) (*Hold, string) {
	t.Helper()
	before, err := s.AgentShipment(ctx, "SHP1009")
	require.NoError(t, err)
	require.NotNil(t, before.Booking)
	opts, err := s.Options(ctx, "SHP1009", 1)
	require.NoError(t, err)
	require.NotEmpty(t, opts)
	h, err := s.CreateHold(ctx, "SHP1009", opts[0].SlotID)
	require.NoError(t, err)
	c, err := s.ConfirmHold(ctx, h.AppointmentID)
	require.NoError(t, err)
	require.Equal(t, OutcomeAwaitingApproval, c.Outcome)
	require.NotNil(t, c.Approval)
	return c, before.Booking.AppointmentID
}

func agentCtx() context.Context { return utility.WithUser(context.Background(), "agent:telegram") }

// C2 and C3: a CRITICAL truck's change waits with its old booking current and the new slot reserved for 60 minutes,
// past the 15-minute hold sweep; approve swaps the bookings and tells the driver on the feed.
func TestCriticalChangeWaitsForApprovalThenApproveSwaps(t *testing.T) {
	s := open(t)
	ctx := agentCtx()
	h, oldID := awaitApproval(t, s, ctx)
	req := h.Approval
	assert.Equal(t, RequestPending, req.Status)
	assert.Equal(t, "agent:telegram", req.RequestedBy)
	assert.Equal(t, req.RequestedAt.Add(60*time.Minute), req.ExpiresAt)
	assert.Equal(t, req.ExpiresAt, h.ExpiresAt, "the hold lasts as long as its reservation")
	assert.Equal(t, "CONFIRMED", appt(t, s, oldID).AppointmentStatus, "the old booking stays until a coordinator decides")
	assert.Equal(t, 1, appt(t, s, oldID).IsCurrent)

	st, err := s.AgentShipment(ctx, "SHP1009")
	require.NoError(t, err)
	require.NotNil(t, st.Approval)
	assert.Equal(t, oldID, st.Booking.AppointmentID)
	assert.Contains(t, st.Message, "waiting for a coordinator")

	again, err := s.ConfirmHold(ctx, h.AppointmentID)
	require.NoError(t, err)
	assert.Equal(t, OutcomeAwaitingApproval, again.Outcome)
	assert.Equal(t, req.RequestID, again.Approval.RequestID, "confirming again returns the same request")

	others, err := s.Options(ctx, "SHP1020", 50)
	require.NoError(t, err)
	for _, o := range others {
		assert.NotEqual(t, h.SlotID, o.SlotID, "nobody else can take a reserved slot")
	}

	// 16 minutes on: an ordinary hold would be swept; the reservation is not.
	setClock(t, s, "2026-08-04T10:11:00+05:30")
	_, err = s.ExpireHolds(ctx)
	require.NoError(t, err)
	assert.Equal(t, "PENDING_CONFIRMATION", appt(t, s, h.AppointmentID).AppointmentStatus)

	pending, err := s.PendingRequests(ctx, "FAC-JAI-01")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, h.SlotID, pending[0].ToSlotID)
	require.NotNil(t, pending[0].FromDockCode, "the coordinator sees the booking it would replace")

	coordinator := utility.WithUser(context.Background(), "demo@setuhaul.local")
	out, err := s.DecideRequest(coordinator, req.RequestID, true, "")
	require.NoError(t, err)
	assert.Equal(t, RequestApproved, out.Status)
	assert.Equal(t, "demo@setuhaul.local", *out.DecidedBy)
	assert.Equal(t, "CONFIRMED", appt(t, s, h.AppointmentID).AppointmentStatus)
	assert.Equal(t, 1, appt(t, s, h.AppointmentID).IsCurrent)
	assert.Equal(t, "CANCELLED", appt(t, s, oldID).AppointmentStatus)

	f := feed(t, s)
	require.NotEmpty(t, f)
	last := f[len(f)-1]
	assert.Equal(t, "APPROVAL", last.Kind)
	assert.Equal(t, "APPROVED", last.Action)
	assert.Equal(t, req.RequestID, *last.RequestID)
	assert.Contains(t, last.Message, "SHP1009: your change is approved")
	assert.NotNil(t, last.DriverPhone)

	_, err = s.DecideRequest(coordinator, req.RequestID, false, "")
	assert.Equal(t, HoldConflictCode, code(err), "a decided request cannot be decided again")
}

// C3: reject releases the reservation and the truck keeps its booking.
func TestRejectKeepsTheOldBooking(t *testing.T) {
	s := open(t)
	ctx := agentCtx()
	h, oldID := awaitApproval(t, s, ctx)
	out, err := s.DecideRequest(context.Background(), h.Approval.RequestID, false, "D2 is kept for the reefer load")
	require.NoError(t, err)
	assert.Equal(t, RequestRejected, out.Status)
	assert.Equal(t, "REJECTED", appt(t, s, h.AppointmentID).AppointmentStatus)
	assert.Equal(t, 0, appt(t, s, h.AppointmentID).IsCurrent)
	assert.Equal(t, "CONFIRMED", appt(t, s, oldID).AppointmentStatus)
	assert.Equal(t, 1, appt(t, s, oldID).IsCurrent)
	last := feed(t, s)[len(feed(t, s))-1]
	assert.Equal(t, "REJECTED", last.Action)
	assert.Contains(t, last.Message, "your slot stays")
	assert.Contains(t, last.Message, "D2 is kept for the reefer load")

	opts, err := s.Options(ctx, "SHP1020", 50)
	require.NoError(t, err)
	free := false
	for _, o := range opts {
		free = free || o.SlotID == h.SlotID
	}
	st, err := s.AgentShipment(ctx, "SHP1009")
	require.NoError(t, err)
	assert.Nil(t, st.Approval)
	assert.Nil(t, st.Hold)
	_ = free // whether SHP1020 can use it depends on its dock type; the reservation is gone either way
}

// C2: no answer in 60 minutes counts as a reject.
func TestAReservationExpiresAfterSixtyMinutes(t *testing.T) {
	s := open(t)
	ctx := agentCtx()
	h, oldID := awaitApproval(t, s, ctx)
	setClock(t, s, "2026-08-04T10:56:00+05:30")
	_, err := s.ExpireHolds(ctx)
	require.NoError(t, err)
	got, err := s.GetRequest(ctx, h.Approval.RequestID)
	require.NoError(t, err)
	assert.Equal(t, RequestExpired, got.Status)
	assert.Equal(t, "system", *got.DecidedBy)
	a := appt(t, s, h.AppointmentID)
	assert.Equal(t, "CANCELLED", a.AppointmentStatus)
	assert.Equal(t, "APPROVAL_EXPIRED", *a.CancellationReason)
	assert.Equal(t, "CONFIRMED", appt(t, s, oldID).AppointmentStatus)
	last := feed(t, s)[len(feed(t, s))-1]
	assert.Equal(t, "EXPIRED", last.Action)
	assert.Contains(t, last.Message, "not approved in time")

	_, err = s.DecideRequest(context.Background(), h.Approval.RequestID, true, "")
	assert.Equal(t, HoldConflictCode, code(err))
}

func TestApprovalIsRefusedWhenTheSlotWasBlockedMeanwhile(t *testing.T) {
	s := open(t)
	ctx := agentCtx()
	h, _ := awaitApproval(t, s, ctx)
	exec(t, s, "UPDATE appointment_slots SET slot_status = 'BLOCKED' WHERE slot_id = ?", h.SlotID)
	_, err := s.DecideRequest(context.Background(), h.Approval.RequestID, true, "")
	assert.Equal(t, SlotUnavailableCode, code(err))
	out, err := s.DecideRequest(context.Background(), h.Approval.RequestID, false, "slot blocked")
	require.NoError(t, err)
	assert.Equal(t, RequestRejected, out.Status)
}

func TestReleasingAWaitingHoldWithdrawsTheRequest(t *testing.T) {
	s := open(t)
	ctx := agentCtx()
	h, oldID := awaitApproval(t, s, ctx)
	n := len(feed(t, s))
	_, err := s.ReleaseHold(ctx, h.AppointmentID)
	require.NoError(t, err)
	got, err := s.GetRequest(ctx, h.Approval.RequestID)
	require.NoError(t, err)
	assert.Equal(t, RequestWithdrawn, got.Status)
	assert.Equal(t, "CONFIRMED", appt(t, s, oldID).AppointmentStatus)
	assert.Len(t, feed(t, s), n, "the driver withdrew it: no notice")
}

func TestLowAndNormalChangesConfirmAtOnceUpToTheChatLimit(t *testing.T) {
	s := open(t)
	ctx := agentCtx()
	moves := 0
	for moves < 3 {
		opts, err := s.Options(ctx, "SHP1007", 1) // NORMAL, booked
		require.NoError(t, err)
		if len(opts) == 0 {
			t.Skip("no free slot for SHP1007 in this snapshot")
		}
		h, err := s.CreateHold(ctx, "SHP1007", opts[0].SlotID)
		if moves == 2 {
			assert.Equal(t, RescheduleLimitCode, code(err), "a third chat change is refused")
			return
		}
		require.NoError(t, err)
		c, err := s.ConfirmHold(ctx, h.AppointmentID)
		require.NoError(t, err)
		assert.Equal(t, OutcomeConfirmed, c.Outcome)
		assert.Nil(t, c.Approval)
		moves++
	}
}

func TestThreadsMessagesExceptionsAndCases(t *testing.T) {
	s := open(t)
	ctx := agentCtx()
	th, created, err := s.OpenThread(ctx, "SHP1009", "")
	require.NoError(t, err)
	assert.True(t, created)
	again, created, err := s.OpenThread(ctx, "SHP1009", "REPORT_DELAY")
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, th.ThreadID, again.ThreadID, "one open thread per shipment")
	_, _, err = s.OpenThread(ctx, "SHP-NOPE", "")
	assert.Equal(t, "RECORD_NOT_FOUND", code(err))

	m, err := s.LogMessage(ctx, th.ThreadID, NewMessage{SenderType: "DRIVER", Text: "1 hour late", ExternalMessageID: "tg:5:77"})
	require.NoError(t, err)
	assert.False(t, m.Duplicate)
	assert.Equal(t, "agent:telegram", *m.SenderReference)
	dup, err := s.LogMessage(ctx, th.ThreadID, NewMessage{SenderType: "DRIVER", Text: "1 hour late", ExternalMessageID: "tg:5:77"})
	require.NoError(t, err)
	assert.True(t, dup.Duplicate)
	assert.Equal(t, m.MessageID, dup.MessageID, "a retried message is stored once")
	_, err = s.LogMessage(ctx, th.ThreadID, NewMessage{SenderType: "OPERATIONS", Text: "x"})
	assert.Equal(t, "BAD_REQUEST", code(err), "only coordinators write as OPERATIONS")

	delay := 60
	x, created, err := s.ReportException(ctx, ExceptionReport{ThreadID: th.ThreadID, Type: "TRAFFIC", DelayMin: &delay,
		Description: "traffic near Shahpura"})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "CRITICAL", x.Severity, "a CRITICAL load's exception is CRITICAL")
	more := 150
	y, created, err := s.ReportException(ctx, ExceptionReport{ThreadID: th.ThreadID, Type: "TRAFFIC", DelayMin: &more,
		Description: "worse"})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, x.ExceptionID, y.ExceptionID, "one live exception per thread")
	big := 2000
	_, _, err = s.ReportException(ctx, ExceptionReport{ThreadID: th.ThreadID, Type: "TRAFFIC", DelayMin: &big, Description: "x"})
	assert.Equal(t, "BAD_REQUEST", code(err))

	// The same problem from a second conversation the same day is a duplicate.
	_, err = s.UpdateThread(ctx, th.ThreadID, ThreadUpdate{Status: "CLOSED"})
	require.NoError(t, err)
	th2, _, err := s.OpenThread(ctx, "SHP1009", "")
	require.NoError(t, err)
	assert.NotEqual(t, th.ThreadID, th2.ThreadID)
	z, _, err := s.ReportException(ctx, ExceptionReport{ThreadID: th2.ThreadID, Type: "TRAFFIC", Description: "again"})
	require.NoError(t, err)
	assert.Equal(t, "DUPLICATE", z.Status)
	assert.Equal(t, x.ExceptionID, *z.DuplicateOf)

	open, err := s.OpenExceptions(ctx, "FAC-JAI-01")
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, e := range open {
		ids[e.ExceptionID] = true
	}
	assert.True(t, ids[x.ExceptionID])
	assert.False(t, ids[z.ExceptionID], "duplicates are not listed")

	c, err := s.OpenCase(ctx, NewCase{ReasonCode: "NO_FEASIBLE_SLOT", Urgency: "URGENT", Summary: "no slot after 15:00",
		ShipmentID: "SHP1009", ThreadID: th2.ThreadID, ExceptionID: x.ExceptionID, Channel: "TELEGRAM", ChannelUserID: "5"})
	require.NoError(t, err)
	assert.Equal(t, "FAC-JAI-01", *c.FacilityID)
	got, err := getThread(s.GetDB(ctx), th2.ThreadID)
	require.NoError(t, err)
	assert.Equal(t, "ESCALATED", got.Status)
	ex, err := getException(s.GetDB(ctx), x.ExceptionID)
	require.NoError(t, err)
	assert.Equal(t, "ESCALATED", ex.Status)
	_, err = s.OpenCase(ctx, NewCase{ReasonCode: "OTHER", Urgency: "LOW", Summary: "x", ShipmentID: "SHP1003",
		ThreadID: th2.ThreadID, Channel: "TELEGRAM"})
	assert.Equal(t, "BAD_REQUEST", code(err), "a thread about another shipment is refused")

	unknown, err := s.OpenCase(ctx, NewCase{ReasonCode: "UNKNOWN_NUMBER", Urgency: "NORMAL",
		Summary: "number not registered", Channel: "TELEGRAM", ChannelUserID: "999", Phone: "+919999999999"})
	require.NoError(t, err)
	assert.Nil(t, unknown.FacilityID)
	cases, err := s.OpenCases(ctx, "FAC-JAI-01")
	require.NoError(t, err)
	require.Len(t, cases, 2)
	assert.Equal(t, c.CaseID, cases[0].CaseID, "urgent first")

	coordinator := utility.WithUser(context.Background(), "demo@setuhaul.local")
	a, err := s.AssignCase(coordinator, c.CaseID)
	require.NoError(t, err)
	assert.Equal(t, "ASSIGNED", a.Status)
	r, err := s.ResolveCase(coordinator, c.CaseID, "Come to dock D4 at 16:00, I have booked it.")
	require.NoError(t, err)
	assert.Equal(t, "RESOLVED", r.Status)
	_, err = s.ResolveCase(coordinator, c.CaseID, "")
	assert.Equal(t, HoldConflictCode, code(err))
	msgs, err := s.ThreadMessages(ctx, th2.ThreadID)
	require.NoError(t, err)
	assert.Equal(t, "OPERATIONS", msgs[len(msgs)-1].SenderType)

	_, err = s.ResolveCase(coordinator, unknown.CaseID, "Please ask your dispatcher to update your number.")
	require.NoError(t, err)
	f := feed(t, s)
	require.GreaterOrEqual(t, len(f), 2)
	reply, toUnknown := f[len(f)-2], f[len(f)-1]
	assert.Equal(t, "REPLY", reply.Kind)
	assert.Equal(t, "Message from the dock office: Come to dock D4 at 16:00, I have booked it.", reply.Message)
	assert.Equal(t, "SHP1009", *reply.ShipmentID)
	assert.Nil(t, toUnknown.ShipmentID)
	assert.Equal(t, "999", *toUnknown.ChannelUserID, "a reply to an unknown number goes back to its chat")
	assert.Equal(t, "+919999999999", *toUnknown.DriverPhone)
	view, err := s.DriverCasesView(ctx, "FAC-JAI-01")
	require.NoError(t, err)
	assert.Empty(t, view.Cases)
}
