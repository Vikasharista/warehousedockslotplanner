package store

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/errors"
	"github.com/Vikasharista/warehousedockslotplanner/services/core-lib/utility"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// The driver agent's records: chat threads and messages, driver exceptions and coordinator cases. The agent writes
// them through /api/agent/*; coordinators read and close them on the Driver cases screen.

var (
	threadStatuses = set("OPEN", "WAITING_FOR_DRIVER", "WAITING_FOR_WAREHOUSE", "RESOLVED", "ESCALATED", "CLOSED")
	threadIntents  = set("REPORT_DELAY", "ASK_SLOT_OPTIONS", "CHECK_STATUS", "EARLY_ARRIVAL", "GENERAL_QUESTION", "UNKNOWN")
	agentSenders   = set("DRIVER", "AGENT", "SYSTEM")
	exceptionTypes = set("DELAY", "BREAKDOWN", "TRAFFIC", "WEATHER", "EARLY_ARRIVAL", "DOCK_UNAVAILABLE", "UNKNOWN")
	// DUPLICATE is the server's to set; the agent may move an exception through the others.
	exceptionStatuses = set("OPEN", "NEEDS_INFORMATION", "SLOT_OPTIONS_SHARED", "WAITING_CONFIRMATION", "RESOLVED",
		"ESCALATED", "CANCELLED")
	caseUrgencies = set("LOW", "NORMAL", "URGENT")
	caseChannels  = set("TELEGRAM", "WHATSAPP", "SIM", "CONSOLE")
	reasonPattern = regexp.MustCompile(`^[A-Z][A-Z_]{1,39}$`)
)

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

func newID(prefix string) string { return prefix + "-" + strings.ToUpper(uuid.NewString()[:13]) }

func actor(ctx context.Context) string {
	if u, _ := utility.GetUserID(ctx); u != "" {
		return u
	}
	return "unknown"
}

// Thread is one driver conversation about a shipment.
type Thread struct {
	ThreadID   string     `json:"threadId"`
	DriverID   string     `json:"driverId"`
	ShipmentID *string    `json:"shipmentId,omitempty"`
	OpenedAt   time.Time  `json:"openedAt"`
	ClosedAt   *time.Time `json:"closedAt,omitempty"`
	Status     string     `json:"status"`
	Intent     string     `json:"intent"`
}

func getThread(db *gorm.DB, id string) (*Thread, error) {
	var out []Thread
	if err := db.Raw(`SELECT thread_id, driver_id, shipment_id, opened_at, closed_at, thread_status AS status,
		thread_intent AS intent FROM chat_threads WHERE thread_id = ?`, id).Scan(&out).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(out) == 0 {
		return nil, errors.NewRecordNotFoundError("no chat thread " + id)
	}
	return &out[0], nil
}

// OpenThread returns the shipment's open conversation, or opens one (created = true). The driver is the shipment's.
func (s *Store) OpenThread(ctx context.Context, shipmentID, intent string) (*Thread, bool, error) {
	if intent == "" {
		intent = "UNKNOWN"
	}
	if !threadIntents[intent] {
		return nil, false, errors.NewBadRequestError("unknown intent " + intent)
	}
	if err := s.shipmentOpen(ctx, shipmentID); err != nil {
		return nil, false, err
	}
	var out *Thread
	created := false
	err := s.DoInTransaction(ctx, func(ctx context.Context) error {
		db := s.GetDB(ctx)
		// One open thread per shipment, even with two agents asking at once.
		if err := db.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "setuhaul.thread:"+shipmentID).Error; err != nil {
			return errors.RepositoryError(err)
		}
		var ids []string
		if err := db.Raw(`SELECT thread_id FROM chat_threads WHERE shipment_id = ?
			AND thread_status NOT IN ('RESOLVED', 'CLOSED') ORDER BY opened_at DESC LIMIT 1`, shipmentID).
			Scan(&ids).Error; err != nil {
			return errors.RepositoryError(err)
		}
		id := ""
		if len(ids) > 0 {
			id = ids[0]
		} else {
			id, created = newID("THR"), true
			if err := db.Exec(`INSERT INTO chat_threads (thread_id, driver_id, shipment_id, opened_at, thread_status,
					thread_intent)
				SELECT ?, driver_id, shipment_id, setuhaul_now(), 'OPEN', ? FROM shipments WHERE shipment_id = ?`,
				id, intent, shipmentID).Error; err != nil {
				return errors.RepositoryError(err)
			}
		}
		var err error
		out, err = getThread(db, id)
		return err
	})
	return out, created, err
}

// ThreadUpdate changes a thread's status or intent (either may be empty).
type ThreadUpdate struct {
	Status string `json:"status"`
	Intent string `json:"intent"`
}

// UpdateThread sets a thread's status (RESOLVED and CLOSED stamp closed_at) and intent.
func (s *Store) UpdateThread(ctx context.Context, id string, in ThreadUpdate) (*Thread, error) {
	if in.Status == "" && in.Intent == "" {
		return nil, errors.NewBadRequestError("send status or intent")
	}
	if in.Status != "" && !threadStatuses[in.Status] {
		return nil, errors.NewBadRequestError("unknown thread status " + in.Status)
	}
	if in.Intent != "" && !threadIntents[in.Intent] {
		return nil, errors.NewBadRequestError("unknown intent " + in.Intent)
	}
	db := s.GetDB(ctx)
	if _, err := getThread(db, id); err != nil {
		return nil, err
	}
	if err := db.Exec(`UPDATE chat_threads SET
			thread_status = COALESCE(NULLIF(?, ''), thread_status),
			thread_intent = COALESCE(NULLIF(?, ''), thread_intent),
			closed_at = CASE WHEN COALESCE(NULLIF(?, ''), thread_status) IN ('RESOLVED', 'CLOSED')
			                 THEN COALESCE(closed_at, setuhaul_now()) ELSE NULL END
		WHERE thread_id = ?`, in.Status, in.Intent, in.Status, id).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	return getThread(db, id)
}

// NewMessage is one message to log on a thread.
type NewMessage struct {
	SenderType          string     `json:"senderType"`
	Text                string     `json:"text"`
	ExternalMessageID   string     `json:"externalMessageId"`
	ParsedIntent        string     `json:"parsedIntent"`
	ExtractedEta        *time.Time `json:"extractedEta"`
	RequiresHumanReview bool       `json:"requiresHumanReview"`
}

// Message is a logged chat message.
type Message struct {
	MessageID           string     `json:"messageId"`
	ThreadID            string     `json:"threadId"`
	SenderType          string     `json:"senderType"`
	SenderReference     *string    `json:"senderReference,omitempty"`
	Text                string     `json:"text"`
	SentAt              time.Time  `json:"sentAt"`
	ExternalMessageID   *string    `json:"externalMessageId,omitempty"`
	ParsedIntent        *string    `json:"parsedIntent,omitempty"`
	ExtractedEta        *time.Time `json:"extractedEta,omitempty"`
	RequiresHumanReview bool       `json:"requiresHumanReview"`
	// Duplicate is true when this external message id was already logged on the thread (a webhook retry).
	Duplicate bool `json:"duplicate"`
}

const messageCols = `chat_message_id AS message_id, thread_id, sender_type, sender_reference, message_text AS text,
	message_ts AS sent_at, external_message_id, parsed_intent, extracted_eta_ts AS extracted_eta,
	requires_human_review = 1 AS requires_human_review`

// LogMessage stores a driver, agent or system message. A repeated external message id on the same thread is stored
// once: the earlier message comes back with duplicate = true.
func (s *Store) LogMessage(ctx context.Context, threadID string, in NewMessage) (*Message, error) {
	if !agentSenders[in.SenderType] {
		return nil, errors.NewBadRequestError("senderType must be DRIVER, AGENT or SYSTEM")
	}
	if in.Text == "" || len(in.Text) > 4000 {
		return nil, errors.NewBadRequestError("text is required, at most 4000 characters")
	}
	if len(in.ExternalMessageID) > 200 || len(in.ParsedIntent) > 40 {
		return nil, errors.NewBadRequestError("externalMessageId (200) or parsedIntent (40) is too long")
	}
	db := s.GetDB(ctx)
	t, err := getThread(db, threadID)
	if err != nil {
		return nil, err
	}
	sender := actor(ctx) // who logged it; a driver's own message names the driver
	if in.SenderType == "DRIVER" {
		sender = t.DriverID
	}
	nullable := func(v string) interface{} {
		if v == "" {
			return nil
		}
		return v
	}
	review := 0
	if in.RequiresHumanReview {
		review = 1
	}
	id := newID("MSG")
	res := db.Exec(`INSERT INTO chat_messages (chat_message_id, thread_id, sender_type, sender_reference, message_text,
			message_ts, external_message_id, is_duplicate, parsed_intent, extracted_eta_ts, requires_human_review)
		VALUES (?, ?, ?, ?, ?, setuhaul_now(), ?, 0, ?, ?, ?)
		ON CONFLICT (thread_id, external_message_id) WHERE external_message_id IS NOT NULL DO NOTHING`,
		id, threadID, in.SenderType, sender, in.Text, nullable(in.ExternalMessageID), nullable(in.ParsedIntent),
		in.ExtractedEta, review)
	if res.Error != nil {
		return nil, errors.RepositoryError(res.Error)
	}
	var out []Message
	duplicate := res.RowsAffected == 0
	q := db.Raw(`SELECT `+messageCols+` FROM chat_messages WHERE chat_message_id = ?`, id)
	if duplicate {
		q = db.Raw(`SELECT `+messageCols+` FROM chat_messages WHERE thread_id = ? AND external_message_id = ?`,
			threadID, in.ExternalMessageID)
	}
	if err := q.Scan(&out).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(out) == 0 {
		return nil, errors.NewInternalServerError("logged message not found")
	}
	out[0].Duplicate = duplicate
	return &out[0], nil
}

// ThreadMessages returns a thread's messages, oldest first (coordinators reading a conversation).
func (s *Store) ThreadMessages(ctx context.Context, threadID string) ([]Message, error) {
	db := s.GetDB(ctx)
	if _, err := getThread(db, threadID); err != nil {
		return nil, err
	}
	out := []Message{}
	err := db.Raw(`SELECT `+messageCols+` FROM chat_messages WHERE thread_id = ? AND message_ts <= setuhaul_now()
		ORDER BY message_ts, chat_message_id`,
		threadID).Scan(&out).Error
	return out, errors.RepositoryError(err)
}

// ExceptionReport is what the agent knows about a driver's problem.
type ExceptionReport struct {
	ThreadID           string     `json:"threadId"`
	Type               string     `json:"type"`
	DelayMin           *int       `json:"delayMin"`
	DeclaredEta        *time.Time `json:"declaredEta"`
	EarliestAcceptable *time.Time `json:"earliestAcceptable"`
	LatestAcceptable   *time.Time `json:"latestAcceptable"`
	Description        string     `json:"description"`
	Status             string     `json:"status"`
}

// DriverException is a stored driver exception.
type DriverException struct {
	ExceptionID        string     `json:"exceptionId"`
	ThreadID           string     `json:"threadId"`
	ShipmentID         *string    `json:"shipmentId,omitempty"`
	DriverID           string     `json:"driverId"`
	DriverName         string     `json:"driverName"`
	PriorityCode       *string    `json:"priorityCode,omitempty"`
	Type               string     `json:"type"`
	ReportedAt         time.Time  `json:"reportedAt"`
	DelayMin           *int       `json:"delayMin,omitempty"`
	DeclaredEta        *time.Time `json:"declaredEta,omitempty"`
	EarliestAcceptable *time.Time `json:"earliestAcceptable,omitempty"`
	LatestAcceptable   *time.Time `json:"latestAcceptable,omitempty"`
	Severity           string     `json:"severity"`
	Status             string     `json:"status"`
	Description        string     `json:"description"`
	DuplicateOf        *string    `json:"duplicateOf,omitempty"`
}

const exceptionSQL = `
	SELECT x.exception_id, x.thread_id, x.shipment_id, x.driver_id, d.driver_name, s.priority_code,
	       x.exception_type AS type, x.reported_at, x.reported_delay_min AS delay_min, x.declared_eta_ts AS declared_eta,
	       x.earliest_acceptable_ts AS earliest_acceptable, x.latest_acceptable_ts AS latest_acceptable,
	       x.severity_code AS severity, x.exception_status AS status, x.description,
	       CASE WHEN x.exception_status = 'DUPLICATE' THEN (
	           SELECT o.exception_id FROM driver_exceptions o WHERE o.dedupe_key = x.dedupe_key
	             AND o.exception_id <> x.exception_id AND o.exception_status NOT IN ('DUPLICATE', 'CANCELLED')
	           ORDER BY o.reported_at LIMIT 1) END AS duplicate_of
	FROM driver_exceptions x
	JOIN drivers d ON d.driver_id = x.driver_id
	LEFT JOIN shipments s ON s.shipment_id = x.shipment_id`

func getException(db *gorm.DB, id string) (*DriverException, error) {
	var out []DriverException
	if err := db.Raw(exceptionSQL+` WHERE x.exception_id = ?`, id).Scan(&out).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(out) == 0 {
		return nil, errors.NewRecordNotFoundError("no driver exception " + id)
	}
	return &out[0], nil
}

// severity follows the agent's rule: a CRITICAL load is CRITICAL; a HIGH load or a delay of 2 hours is HIGH; a delay
// of 30 minutes is MEDIUM; anything else LOW.
func severity(priority string, delayMin *int) string {
	d := 0
	if delayMin != nil {
		d = *delayMin
	}
	switch {
	case priority == "CRITICAL":
		return "CRITICAL"
	case priority == "HIGH" || d >= 120:
		return "HIGH"
	case d >= 30:
		return "MEDIUM"
	default:
		return "LOW"
	}
}

// ReportException records a driver's problem on a thread. A thread has one live exception, updated in place; a new
// one for the same shipment and type on the same day as a live one from another thread is stored as DUPLICATE. The
// server sets the severity. created = true when a new row was written.
func (s *Store) ReportException(ctx context.Context, in ExceptionReport) (*DriverException, bool, error) {
	if !exceptionTypes[in.Type] {
		return nil, false, errors.NewBadRequestError("unknown exception type " + in.Type)
	}
	if in.Status == "" {
		in.Status = "OPEN"
	}
	if !exceptionStatuses[in.Status] {
		return nil, false, errors.NewBadRequestError("unknown exception status " + in.Status)
	}
	if in.DelayMin != nil && (*in.DelayMin < 0 || *in.DelayMin > 1440) {
		return nil, false, errors.NewBadRequestError("delayMin must be between 0 and 1440")
	}
	if in.Description == "" || len(in.Description) > 1000 {
		return nil, false, errors.NewBadRequestError("description is required, at most 1000 characters")
	}
	var out *DriverException
	created := false
	err := s.DoInTransaction(ctx, func(ctx context.Context) error {
		db := s.GetDB(ctx)
		t, err := getThread(db, in.ThreadID)
		if err != nil {
			return err
		}
		if t.ShipmentID == nil {
			return errors.NewBadRequestError("thread " + in.ThreadID + " has no shipment")
		}
		if err := db.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "setuhaul.exception:"+*t.ShipmentID).Error; err != nil {
			return errors.RepositoryError(err)
		}
		var priority string
		if err := db.Raw("SELECT priority_code FROM shipments WHERE shipment_id = ?", *t.ShipmentID).Scan(&priority).Error; err != nil {
			return errors.RepositoryError(err)
		}
		var key string
		if err := db.Raw(`SELECT ? || ':' || ? || ':' || to_char(setuhaul_now() AT TIME ZONE 'Asia/Kolkata', 'YYYYMMDD')`,
			*t.ShipmentID, in.Type).Scan(&key).Error; err != nil {
			return errors.RepositoryError(err)
		}
		sev := severity(priority, in.DelayMin)
		var mine []string
		if err := db.Raw(`SELECT exception_id FROM driver_exceptions WHERE thread_id = ?
			AND exception_status NOT IN ('RESOLVED', 'CANCELLED', 'DUPLICATE') ORDER BY reported_at DESC LIMIT 1`,
			in.ThreadID).Scan(&mine).Error; err != nil {
			return errors.RepositoryError(err)
		}
		id := ""
		if len(mine) > 0 {
			id = mine[0]
			if err := db.Exec(`UPDATE driver_exceptions SET exception_type = ?, reported_delay_min = ?,
					declared_eta_ts = ?, earliest_acceptable_ts = ?, latest_acceptable_ts = ?, severity_code = ?,
					exception_status = ?, description = ?, dedupe_key = ?
				WHERE exception_id = ?`, in.Type, in.DelayMin, in.DeclaredEta, in.EarliestAcceptable,
				in.LatestAcceptable, sev, in.Status, in.Description, key, id).Error; err != nil {
				return errors.RepositoryError(err)
			}
		} else {
			var others int64
			if err := db.Raw(`SELECT count(*) FROM driver_exceptions WHERE dedupe_key = ?
				AND exception_status NOT IN ('RESOLVED', 'CANCELLED', 'DUPLICATE')`, key).Scan(&others).Error; err != nil {
				return errors.RepositoryError(err)
			}
			status := in.Status
			if others > 0 {
				status = "DUPLICATE"
			}
			id, created = newID("EXC"), true
			if err := db.Exec(`INSERT INTO driver_exceptions (exception_id, shipment_id, driver_id, thread_id,
					exception_type, reported_at, reported_delay_min, declared_eta_ts, earliest_acceptable_ts,
					latest_acceptable_ts, severity_code, exception_status, description, dedupe_key)
				VALUES (?, ?, ?, ?, ?, setuhaul_now(), ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, *t.ShipmentID, t.DriverID, in.ThreadID, in.Type, in.DelayMin, in.DeclaredEta,
				in.EarliestAcceptable, in.LatestAcceptable, sev, status, in.Description, key).Error; err != nil {
				return errors.RepositoryError(err)
			}
		}
		out, err = getException(db, id)
		return err
	})
	return out, created, err
}

// SetExceptionStatus moves an exception on (the agent: options shared, waiting, resolved, escalated, cancelled).
func (s *Store) SetExceptionStatus(ctx context.Context, id, status string) (*DriverException, error) {
	if !exceptionStatuses[status] {
		return nil, errors.NewBadRequestError("unknown exception status " + status)
	}
	db := s.GetDB(ctx)
	x, err := getException(db, id)
	if err != nil {
		return nil, err
	}
	if x.Status == "DUPLICATE" && status != "CANCELLED" && status != "RESOLVED" {
		return nil, errors.New(HoldConflictCode, "exception "+id+" is a duplicate; resolve or cancel it")
	}
	if err := db.Exec("UPDATE driver_exceptions SET exception_status = ? WHERE exception_id = ?", status, id).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	return getException(db, id)
}

// OpenExceptions lists a facility's live driver exceptions, most severe and oldest first.
func (s *Store) OpenExceptions(ctx context.Context, facilityID string) ([]DriverException, error) {
	out := []DriverException{}
	err := s.GetDB(ctx).Raw(exceptionSQL+` WHERE s.destination_facility_id = ?
		AND x.exception_status NOT IN ('RESOLVED', 'CANCELLED', 'DUPLICATE') AND x.reported_at <= setuhaul_now()
		ORDER BY CASE x.severity_code WHEN 'CRITICAL' THEN 0 WHEN 'HIGH' THEN 1 WHEN 'MEDIUM' THEN 2 ELSE 3 END,
		         x.reported_at`, facilityID).Scan(&out).Error
	return out, errors.RepositoryError(err)
}

// NewCase is an escalation from the agent to a coordinator.
type NewCase struct {
	ReasonCode    string `json:"reasonCode"`
	Urgency       string `json:"urgency"`
	Summary       string `json:"summary"`
	ShipmentID    string `json:"shipmentId"`
	ThreadID      string `json:"threadId"`
	ExceptionID   string `json:"exceptionId"`
	Channel       string `json:"channel"`
	ChannelUserID string `json:"channelUserId"`
	Phone         string `json:"phone"`
}

// Case is a coordinator case.
type Case struct {
	CaseID        string     `json:"caseId"`
	FacilityID    *string    `json:"facilityId,omitempty"`
	ShipmentID    *string    `json:"shipmentId,omitempty"`
	DriverID      *string    `json:"driverId,omitempty"`
	DriverName    *string    `json:"driverName,omitempty"`
	PriorityCode  *string    `json:"priorityCode,omitempty"`
	ThreadID      *string    `json:"threadId,omitempty"`
	ExceptionID   *string    `json:"exceptionId,omitempty"`
	Channel       string     `json:"channel"`
	ChannelUserID *string    `json:"channelUserId,omitempty"`
	Phone         *string    `json:"phone,omitempty"`
	ReasonCode    string     `json:"reasonCode"`
	Urgency       string     `json:"urgency"`
	Summary       string     `json:"summary"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"createdAt"`
	CreatedBy     string     `json:"createdBy"`
	AssignedTo    *string    `json:"assignedTo,omitempty"`
	ResolvedAt    *time.Time `json:"resolvedAt,omitempty"`
	ResolvedBy    *string    `json:"resolvedBy,omitempty"`
	ReplyText     *string    `json:"replyText,omitempty"`
	// Reused is true when OpenCase added to the thread's open case instead of opening a new one.
	Reused bool `json:"reused,omitempty" gorm:"-"`
}

const caseSQL = `
	SELECT c.case_id, c.facility_id, c.shipment_id, c.driver_id, d.driver_name, s.priority_code, c.thread_id,
	       c.exception_id, c.channel, c.channel_user_id, c.phone, c.reason_code, c.urgency, c.summary,
	       c.case_status AS status, c.created_at, c.created_by, c.assigned_to, c.resolved_at, c.resolved_by, c.reply_text
	FROM coordinator_cases c
	LEFT JOIN drivers d ON d.driver_id = c.driver_id
	LEFT JOIN shipments s ON s.shipment_id = c.shipment_id`

func getCase(db *gorm.DB, id string) (*Case, error) {
	var out []Case
	if err := db.Raw(caseSQL+` WHERE c.case_id = ?`, id).Scan(&out).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(out) == 0 {
		return nil, errors.NewRecordNotFoundError("no case " + id)
	}
	return &out[0], nil
}

// OpenCase escalates to a coordinator. The facility and driver come from the shipment; a thread is marked ESCALATED
// and so is the exception, if given. An unknown number has no shipment: its case carries the channel and phone only.
func (s *Store) OpenCase(ctx context.Context, in NewCase) (*Case, error) {
	switch {
	case !reasonPattern.MatchString(in.ReasonCode):
		return nil, errors.NewBadRequestError("reasonCode must be upper-case letters and underscores")
	case !caseUrgencies[in.Urgency]:
		return nil, errors.NewBadRequestError("urgency must be LOW, NORMAL or URGENT")
	case in.Summary == "" || len(in.Summary) > 1000:
		return nil, errors.NewBadRequestError("summary is required, at most 1000 characters")
	case !caseChannels[in.Channel]:
		return nil, errors.NewBadRequestError("channel must be TELEGRAM, WHATSAPP, SIM or CONSOLE")
	case len(in.ChannelUserID) > 100 || len(in.Phone) > 30:
		return nil, errors.NewBadRequestError("channelUserId (100) or phone (30) is too long")
	case in.ShipmentID == "" && (in.ThreadID != "" || in.ExceptionID != ""):
		return nil, errors.NewBadRequestError("a thread or exception needs its shipmentId")
	}
	var out *Case
	err := s.DoInTransaction(ctx, func(ctx context.Context) error {
		db := s.GetDB(ctx)
		var facility, driver interface{}
		if in.ShipmentID != "" {
			var rows []struct{ FacilityID, DriverID string }
			if err := db.Raw(`SELECT destination_facility_id AS facility_id, driver_id FROM shipments WHERE shipment_id = ?`,
				in.ShipmentID).Scan(&rows).Error; err != nil {
				return errors.RepositoryError(err)
			}
			if len(rows) == 0 {
				return errors.NewRecordNotFoundError("no shipment " + in.ShipmentID)
			}
			facility, driver = rows[0].FacilityID, rows[0].DriverID
		}
		if in.ThreadID != "" {
			t, err := getThread(db, in.ThreadID)
			if err != nil {
				return err
			}
			if t.ShipmentID == nil || *t.ShipmentID != in.ShipmentID {
				return errors.NewBadRequestError("thread " + in.ThreadID + " is not about " + in.ShipmentID)
			}
		}
		if in.ExceptionID != "" {
			x, err := getException(db, in.ExceptionID)
			if err != nil {
				return err
			}
			if x.ShipmentID == nil || *x.ShipmentID != in.ShipmentID {
				return errors.NewBadRequestError("exception " + in.ExceptionID + " is not about " + in.ShipmentID)
			}
		}
		if in.ThreadID != "" {
			reused, err := addToOpenCase(db, in)
			if err != nil || reused != nil {
				out = reused
				return err
			}
		}
		opt := func(v string) interface{} {
			if v == "" {
				return nil
			}
			return v
		}
		id := newID("CASE")
		if err := db.Exec(`INSERT INTO coordinator_cases (case_id, facility_id, shipment_id, driver_id, thread_id,
				exception_id, channel, channel_user_id, phone, reason_code, urgency, summary, case_status, created_at,
				created_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'OPEN', setuhaul_now(), ?)`,
			id, facility, opt(in.ShipmentID), driver, opt(in.ThreadID), opt(in.ExceptionID), in.Channel,
			opt(in.ChannelUserID), opt(in.Phone), in.ReasonCode, in.Urgency, in.Summary, actor(ctx)).Error; err != nil {
			return errors.RepositoryError(err)
		}
		if in.ThreadID != "" {
			if err := db.Exec(`UPDATE chat_threads SET thread_status = 'ESCALATED' WHERE thread_id = ?`, in.ThreadID).Error; err != nil {
				return errors.RepositoryError(err)
			}
		}
		if in.ExceptionID != "" {
			if err := db.Exec(`UPDATE driver_exceptions SET exception_status = 'ESCALATED' WHERE exception_id = ?
				AND exception_status <> 'DUPLICATE'`, in.ExceptionID).Error; err != nil {
				return errors.RepositoryError(err)
			}
		}
		var err error
		out, err = getCase(db, id)
		return err
	})
	return out, err
}

// addToOpenCase keeps one open case per conversation: when the thread already has an unresolved case, the new hand-over
// is added to it (its summary appended, its urgency and reason raised when the new one is more urgent, for example a
// safety message on a case about a missing slot) and that case is returned. Returns nil when the thread has no open
// case. A per-thread advisory lock stops two hand-overs on the same thread from both opening a case.
func addToOpenCase(db *gorm.DB, in NewCase) (*Case, error) {
	if err := db.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "setuhaul.cases:"+in.ThreadID).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	var open []struct{ CaseID, ReasonCode, Urgency string }
	if err := db.Raw(`SELECT case_id, reason_code, urgency FROM coordinator_cases
		WHERE thread_id = ? AND case_status <> 'RESOLVED' ORDER BY created_at LIMIT 1 FOR UPDATE`, in.ThreadID).
		Scan(&open).Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if len(open) == 0 {
		return nil, nil
	}
	c := open[0]
	reason, urgency := c.ReasonCode, c.Urgency
	if urgencyRank[in.Urgency] > urgencyRank[c.Urgency] {
		reason, urgency = in.ReasonCode, in.Urgency
	}
	if err := db.Exec(`UPDATE coordinator_cases SET reason_code = ?, urgency = ?,
			summary = left(summary || E'\n' || ?, 1000), exception_id = COALESCE(exception_id, ?)
		WHERE case_id = ?`, reason, urgency, in.ReasonCode+": "+in.Summary, nullIfEmpty(in.ExceptionID), c.CaseID).
		Error; err != nil {
		return nil, errors.RepositoryError(err)
	}
	if in.ExceptionID != "" {
		if err := db.Exec(`UPDATE driver_exceptions SET exception_status = 'ESCALATED' WHERE exception_id = ?
			AND exception_status <> 'DUPLICATE'`, in.ExceptionID).Error; err != nil {
			return nil, errors.RepositoryError(err)
		}
	}
	out, err := getCase(db, c.CaseID)
	if err != nil {
		return nil, err
	}
	out.Reused = true
	return out, nil
}

// urgencyRank orders case urgencies: a later hand-over can raise a case's urgency but never lower it.
var urgencyRank = map[string]int{"LOW": 1, "NORMAL": 2, "URGENT": 3}

func nullIfEmpty(v string) interface{} {
	if v == "" {
		return nil
	}
	return v
}

// OpenCases lists a facility's unresolved cases and those with no facility (unknown numbers), most urgent and oldest
// first.
func (s *Store) OpenCases(ctx context.Context, facilityID string) ([]Case, error) {
	out := []Case{}
	err := s.GetDB(ctx).Raw(caseSQL+` WHERE (c.facility_id = ? OR c.facility_id IS NULL) AND c.case_status <> 'RESOLVED'
		ORDER BY CASE c.urgency WHEN 'URGENT' THEN 0 WHEN 'NORMAL' THEN 1 ELSE 2 END, c.created_at`, facilityID).
		Scan(&out).Error
	return out, errors.RepositoryError(err)
}

// GetCase returns one case (the agent checks whether its escalation was resolved).
func (s *Store) GetCase(ctx context.Context, id string) (*Case, error) {
	return getCase(s.GetDB(ctx), id)
}

// AssignCase puts an open case on the signed-in coordinator.
func (s *Store) AssignCase(ctx context.Context, id string) (*Case, error) {
	db := s.GetDB(ctx)
	res := db.Exec(`UPDATE coordinator_cases SET case_status = 'ASSIGNED', assigned_to = ?, assigned_at = setuhaul_now()
		WHERE case_id = ? AND case_status <> 'RESOLVED'`, actor(ctx), id)
	if res.Error != nil {
		return nil, errors.RepositoryError(res.Error)
	}
	c, err := getCase(db, id)
	if err != nil {
		return nil, err
	}
	if res.RowsAffected == 0 {
		return nil, errors.New(HoldConflictCode, "case "+id+" is already resolved")
	}
	return c, nil
}

// ResolveCase closes a case. A reply is logged on the conversation and sent to the driver on the change feed (kind
// REPLY); the thread is marked RESOLVED.
func (s *Store) ResolveCase(ctx context.Context, id, reply string) (*Case, error) {
	reply = strings.TrimSpace(reply)
	if len(reply) > 1000 {
		return nil, errors.NewBadRequestError("reply is limited to 1000 characters")
	}
	err := s.DoInTransaction(ctx, func(ctx context.Context) error {
		db := s.GetDB(ctx)
		var replyArg interface{}
		if reply != "" {
			replyArg = reply
		}
		res := db.Exec(`UPDATE coordinator_cases SET case_status = 'RESOLVED', resolved_at = setuhaul_now(),
				resolved_by = ?, reply_text = ?
			WHERE case_id = ? AND case_status <> 'RESOLVED'`, actor(ctx), replyArg, id)
		if res.Error != nil {
			return errors.RepositoryError(res.Error)
		}
		c, err := getCase(db, id)
		if err != nil {
			return err
		}
		if res.RowsAffected == 0 {
			return errors.New(HoldConflictCode, "case "+id+" is already resolved")
		}
		if c.ThreadID != nil {
			if reply != "" {
				if err := db.Exec(`INSERT INTO chat_messages (chat_message_id, thread_id, sender_type, sender_reference,
						message_text, message_ts, is_duplicate, requires_human_review)
					VALUES (?, ?, 'OPERATIONS', ?, ?, setuhaul_now(), 0, 0)`, newID("MSG"), *c.ThreadID, actor(ctx),
					reply).Error; err != nil {
					return errors.RepositoryError(err)
				}
			}
			if err := db.Exec(`UPDATE chat_threads SET thread_status = 'RESOLVED', closed_at = setuhaul_now()
				WHERE thread_id = ? AND thread_status NOT IN ('RESOLVED', 'CLOSED')`, *c.ThreadID).Error; err != nil {
				return errors.RepositoryError(err)
			}
		}
		if reply != "" {
			if err := db.Exec(`INSERT INTO booking_changes (facility_id, shipment_id, action, changed_at, kind, case_id,
					message_text)
				VALUES (?, ?, 'REPLY', setuhaul_now(), 'REPLY', ?, ?)`, c.FacilityID, c.ShipmentID, id, reply).Error; err != nil {
				return errors.RepositoryError(err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return getCase(s.GetDB(ctx), id)
}

// DriverCases is everything the Driver cases screen shows for a facility.
type DriverCases struct {
	Approvals  []ApprovalRequest `json:"approvals"`
	Cases      []Case            `json:"cases"`
	Exceptions []DriverException `json:"exceptions"`
	Now        time.Time         `json:"now"`
}

// DriverCasesView returns a facility's pending approvals, open cases and live driver exceptions.
func (s *Store) DriverCasesView(ctx context.Context, facilityID string) (*DriverCases, error) {
	var out DriverCases
	var err error
	if out.Approvals, err = s.PendingRequests(ctx, facilityID); err != nil {
		return nil, err
	}
	if out.Cases, err = s.OpenCases(ctx, facilityID); err != nil {
		return nil, err
	}
	if out.Exceptions, err = s.OpenExceptions(ctx, facilityID); err != nil {
		return nil, err
	}
	if out.Now, err = s.Now(ctx); err != nil {
		return nil, err
	}
	return &out, nil
}
