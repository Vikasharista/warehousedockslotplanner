package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/Vikasharista/warehousedockslotplanner/services/dock-api/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fake store's phase 7 methods. awaiting makes ConfirmHold answer AWAITING_APPROVAL; seenUser records who called.
var (
	fakeAwaiting bool
	fakeSeenUser string
)

func (f *fakeStore) OpenThread(ctx context.Context, sh, _ string) (*store.Thread, bool, error) {
	fakeSeenUser, _ = utility.GetUserID(ctx)
	return &store.Thread{ThreadID: "THR-1", ShipmentID: &sh, Status: "OPEN"}, sh == "SHP-NEW", f.err
}
func (f *fakeStore) UpdateThread(_ context.Context, id string, in store.ThreadUpdate) (*store.Thread, error) {
	return &store.Thread{ThreadID: id, Status: in.Status}, f.err
}
func (f *fakeStore) LogMessage(_ context.Context, id string, in store.NewMessage) (*store.Message, error) {
	return &store.Message{MessageID: "MSG-1", ThreadID: id, Text: in.Text, Duplicate: in.ExternalMessageID == "seen"}, f.err
}
func (f *fakeStore) ThreadMessages(context.Context, string) ([]store.Message, error) {
	return []store.Message{{MessageID: "MSG-1"}}, f.err
}
func (f *fakeStore) ReportException(_ context.Context, in store.ExceptionReport) (*store.DriverException, bool, error) {
	return &store.DriverException{ExceptionID: "EXC-1", Type: in.Type}, in.ThreadID != "THR-OLD", f.err
}
func (f *fakeStore) SetExceptionStatus(_ context.Context, id, status string) (*store.DriverException, error) {
	return &store.DriverException{ExceptionID: id, Status: status}, f.err
}
func (f *fakeStore) OpenCase(_ context.Context, in store.NewCase) (*store.Case, error) {
	return &store.Case{CaseID: "CASE-1", ReasonCode: in.ReasonCode}, f.err
}
func (f *fakeStore) AssignCase(_ context.Context, id string) (*store.Case, error) {
	return &store.Case{CaseID: id, Status: "ASSIGNED"}, f.err
}
func (f *fakeStore) ResolveCase(_ context.Context, id, reply string) (*store.Case, error) {
	return &store.Case{CaseID: id, Status: "RESOLVED", ReplyText: &reply}, f.err
}
func (f *fakeStore) DecideRequest(_ context.Context, id string, approve bool, _ string) (*store.ApprovalRequest, error) {
	status := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
	return &store.ApprovalRequest{RequestID: id, FacilityID: "FAC-1", Status: status}, f.err
}
func (f *fakeStore) DriverCasesView(context.Context, string) (*store.DriverCases, error) {
	return &store.DriverCases{Approvals: []store.ApprovalRequest{}, Cases: []store.Case{}, Exceptions: []store.DriverException{}}, f.err
}

type confirmStore struct{ fakeStore }

func (c *confirmStore) ConfirmHold(_ context.Context, id string) (*store.Hold, error) {
	if fakeAwaiting {
		return &store.Hold{AppointmentID: id, Status: "PENDING_CONFIRMATION", Outcome: store.OutcomeAwaitingApproval,
			Approval: &store.ApprovalRequest{RequestID: "REQ-1", Status: "PENDING"}}, nil
	}
	return &store.Hold{AppointmentID: id, Status: "CONFIRMED", Outcome: store.OutcomeConfirmed}, nil
}

func TestAgentConversationRoutes(t *testing.T) {
	h := Handler(&fakeStore{}, &fakeSolver{}, allowAll{}, nil)
	code, body, _ := do(t, h, "POST", "/api/agent/threads", `{"shipmentId":"SHP-NEW"}`)
	assert.Equal(t, 201, code)
	assert.Equal(t, "THR-1", body["threadId"])
	code, _, _ = do(t, h, "POST", "/api/agent/threads", `{"shipmentId":"SHP1"}`)
	assert.Equal(t, 200, code, "an open thread is reused")
	code, _, _ = do(t, h, "POST", "/api/agent/threads", `{}`)
	assert.Equal(t, 400, code)
	code, body, _ = do(t, h, "PATCH", "/api/agent/threads/THR-1", `{"status":"RESOLVED"}`)
	assert.Equal(t, 200, code)
	assert.Equal(t, "RESOLVED", body["status"])

	code, _, _ = do(t, h, "POST", "/api/agent/threads/THR-1/messages", `{"senderType":"DRIVER","text":"late","externalMessageId":"tg:1"}`)
	assert.Equal(t, 201, code)
	code, body, _ = do(t, h, "POST", "/api/agent/threads/THR-1/messages", `{"senderType":"DRIVER","text":"late","externalMessageId":"seen"}`)
	assert.Equal(t, 200, code, "a repeated external id answers 200")
	assert.Equal(t, true, body["duplicate"])
	code, _, _ = do(t, h, "POST", "/api/agent/threads/a;b/messages", `{"senderType":"DRIVER","text":"x"}`)
	assert.Equal(t, 400, code)

	code, _, _ = do(t, h, "POST", "/api/agent/exceptions", `{"threadId":"THR-1","type":"DELAY","description":"late"}`)
	assert.Equal(t, 201, code)
	code, _, _ = do(t, h, "POST", "/api/agent/exceptions", `{"threadId":"THR-OLD","type":"DELAY","description":"later"}`)
	assert.Equal(t, 200, code, "the thread's live exception is updated")
	code, body, _ = do(t, h, "PATCH", "/api/agent/exceptions/EXC-1", `{"status":"RESOLVED"}`)
	assert.Equal(t, 200, code)
	assert.Equal(t, "RESOLVED", body["status"])
	code, _, _ = do(t, h, "PATCH", "/api/agent/exceptions/EXC-1", `{"status":"RESOLVED","x":1}`)
	assert.Equal(t, 400, code, "unknown fields are refused")

	code, body, _ = do(t, h, "POST", "/api/agent/cases", `{"reasonCode":"SAFETY","urgency":"URGENT","summary":"accident","channel":"TELEGRAM"}`)
	assert.Equal(t, 201, code)
	assert.Equal(t, "CASE-1", body["caseId"])
	code, _, _ = do(t, h, "POST", "/api/agent/cases", `{"reasonCode":"SAFETY","shipmentId":"a;b"}`)
	assert.Equal(t, 400, code)
}

func TestConfirmAnswers202WhileAChangeWaitsForApproval(t *testing.T) {
	h := Handler(&confirmStore{}, &fakeSolver{}, allowAll{}, nil)
	fakeAwaiting = false
	code, body, _ := do(t, h, "POST", "/api/agent/holds/H1/confirm", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, "CONFIRMED", body["outcome"])
	fakeAwaiting = true
	defer func() { fakeAwaiting = false }()
	for _, path := range []string{"/api/agent/holds/H1/confirm", "/api/holds/H1/confirm"} {
		code, body, _ = do(t, h, "POST", path, "")
		assert.Equal(t, 202, code, path)
		assert.Equal(t, "AWAITING_APPROVAL", body["outcome"], path)
		assert.Equal(t, "REQ-1", body["approvalRequest"].(map[string]interface{})["requestId"], path)
	}
}

func TestCoordinatorRoutes(t *testing.T) {
	st, sv := &fakeStore{}, &fakeSolver{}
	h := Handler(st, sv, allowAll{}, nil)
	code, body, _ := do(t, h, "GET", "/api/facilities/FAC-1/driver-cases", "")
	assert.Equal(t, 200, code)
	assert.Contains(t, body, "approvals")
	code, _, _ = do(t, h, "GET", "/api/facilities/FAC-2/driver-cases", "")
	assert.Equal(t, 404, code)
	code, _, _ = do(t, h, "GET", "/api/threads/THR-1/messages", "")
	assert.Equal(t, 200, code)
	code, body, _ = do(t, h, "POST", "/api/approvals/REQ-1/approve", "")
	assert.Equal(t, 200, code, "the note is optional")
	assert.Equal(t, "APPROVED", body["status"])
	code, body, _ = do(t, h, "POST", "/api/approvals/REQ-1/reject", `{"note":"dock D2 is for the reefer load"}`)
	assert.Equal(t, 200, code)
	assert.Equal(t, "REJECTED", body["status"])
	code, _, _ = do(t, h, "POST", "/api/approvals/REQ-1/reject", `{"why":"x"}`)
	assert.Equal(t, 400, code)
	assert.Eventually(t, func() bool { return len(sv.replans()) == 2 }, 2*time.Second, 10*time.Millisecond, "each decision re-plans")
	code, body, _ = do(t, h, "POST", "/api/cases/CASE-1/assign", "")
	assert.Equal(t, 200, code)
	assert.Equal(t, "ASSIGNED", body["status"])
	code, body, _ = do(t, h, "POST", "/api/cases/CASE-1/resolve", `{"reply":"Use gate 2."}`)
	assert.Equal(t, 200, code)
	assert.Equal(t, "Use gate 2.", body["replyText"])
}

// The access matrix: an agent key opens the agent's conversation routes and nothing a coordinator does; a wrong key
// opens nothing; a signed-in user may use both (the Driver chat console and the Driver cases screen).
func TestPhase7AccessMatrix(t *testing.T) {
	keys, err := ParseAgentKeys("telegram:" + agentKey)
	require.NoError(t, err)
	agentOnly := Handler(&fakeStore{}, &fakeSolver{}, denyAll{}, keys)
	signedIn := Handler(&fakeStore{}, &fakeSolver{}, allowAll{}, keys)
	send := func(useSession bool, method, path, key, body string) int {
		h := agentOnly
		if useSession {
			h = signedIn
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if key != "" {
			r.Header.Set("X-Agent-Key", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	agentRoutes := [][3]string{
		{"POST", "/api/agent/threads", `{"shipmentId":"SHP1"}`},
		{"PATCH", "/api/agent/threads/THR-1", `{"intent":"REPORT_DELAY"}`},
		{"POST", "/api/agent/threads/THR-1/messages", `{"senderType":"AGENT","text":"hi"}`},
		{"POST", "/api/agent/exceptions", `{"threadId":"THR-1","type":"DELAY","description":"x"}`},
		{"PATCH", "/api/agent/exceptions/EXC-1", `{"status":"OPEN"}`},
		{"POST", "/api/agent/cases", `{"reasonCode":"OTHER","urgency":"LOW","summary":"x","channel":"SIM"}`},
	}
	coordinatorRoutes := [][3]string{
		{"GET", "/api/facilities/FAC-1/driver-cases", ""},
		{"GET", "/api/threads/THR-1/messages", ""},
		{"POST", "/api/approvals/REQ-1/approve", ""},
		{"POST", "/api/approvals/REQ-1/reject", ""},
		{"POST", "/api/cases/CASE-1/assign", ""},
		{"POST", "/api/cases/CASE-1/resolve", ""},
	}
	for _, r := range agentRoutes {
		assert.Less(t, send(false, r[0], r[1], agentKey, r[2]), 300, "agent key: "+r[1])
		assert.Equal(t, 401, send(false, r[0], r[1], "wrong-key-wrong-key-wrong-key-wrong", r[2]), "wrong key: "+r[1])
		assert.Equal(t, 401, send(false, r[0], r[1], "", r[2]), "no key, no session: "+r[1])
		assert.Less(t, send(true, r[0], r[1], "", r[2]), 300, "session: "+r[1])
	}
	for _, r := range coordinatorRoutes {
		assert.Equal(t, 401, send(false, r[0], r[1], agentKey, r[2]), "an agent key cannot act as a coordinator: "+r[1])
		assert.Less(t, send(true, r[0], r[1], "", r[2]), 300, "session: "+r[1])
	}

	// The agent's own name is who the store sees.
	send(false, "POST", "/api/agent/threads", agentKey, `{"shipmentId":"SHP1"}`)
	assert.Equal(t, "agent:telegram", fakeSeenUser)
}
