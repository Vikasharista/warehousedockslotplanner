// Typed calls to dock-api. Every path is relative: the dev server proxies /api to the service.

export interface Dock {
  dockId: string;
  dockCode: string;
  dockType: string;
  dockStatus: string;
  maxWeightKg: number;
  refrigerated: boolean;
}

export interface Booking {
  appointmentId: string;
  shipmentId: string;
  slotId: string;
  dockId: string;
  status: string;
  source: string;
  isCurrent: boolean;
  bookedAt: string;
  holdExpiresAt?: string;
  slotStart: string;
  slotEnd: string;
  priorityCode: string;
  vehicleId: string;
  originCity: string;
  loadWeightKg: number;
  unloadMin: number;
  eta?: string;
  etaConfidence?: string;
  etaSource?: string;
  gateIn?: string;
  predictedWaitMin?: number;
  bookingAction?: string;
  flagged: boolean;
}

export interface Block {
  dockId: string;
  kind: string;
  reason: string;
  start: string;
  end?: string;
}

export interface RunSummary {
  runId: string;
  asOf: string;
  trigger: string;
  triggerDetail?: string;
  status: string;
  outcome?: string;
  assigned: number;
  unassigned: number;
  avgWaitMin: number;
  weightedWaitMin: number;
  priorityInversions: number;
  dockUtilisationPct: number;
  solveMs: number;
  committedAt?: string;
  createdAt: string;
}

export interface Board {
  facilityId: string;
  now: string;
  windowStart: string;
  windowEnd: string;
  docks: Dock[];
  bookings: Booking[];
  blocks: Block[];
  lastCommitted?: RunSummary;
  lastRun?: RunSummary;
}

export interface Exception {
  shipmentId: string;
  allocationStatus: string;
  reasonCode?: string;
  detail?: string;
  priorityCode: string;
  vehicleId: string;
  originCity: string;
  loadWeightKg: number;
  earliestReady?: string;
  dockId?: string;
  originalSlotId?: string;
}

export interface Change {
  runId: string;
  committedAt: string;
  trigger: string;
  triggerDetail?: string;
  shipmentId: string;
  action: string;
  fromSlotId?: string;
  fromDockCode?: string;
  fromStart?: string;
  toSlotId?: string;
  toDockCode?: string;
  toStart?: string;
  reasonCode?: string;
}

export interface SlotOption {
  slotId: string;
  dockId: string;
  dockCode: string;
  dockType: string;
  slotStart: string;
  slotEnd: string;
}

export interface Hold {
  appointmentId: string;
  shipmentId: string;
  slotId: string;
  status: string;
  isCurrent: boolean;
  replacesAppointmentId?: string;
  bookedAt: string;
  expiresAt: string;
  confirmedAt?: string;
  cancelledAt?: string;
  /** Set by confirm: CONFIRMED, or AWAITING_APPROVAL (HIGH and CRITICAL changes wait for a coordinator). */
  outcome?: "CONFIRMED" | "AWAITING_APPROVAL";
  approvalRequest?: ApprovalRequest;
}

/** A HIGH or CRITICAL truck's slot change waiting for a coordinator; the slot stays reserved until expiresAt. */
export interface ApprovalRequest {
  requestId: string;
  shipmentId: string;
  holdAppointmentId: string;
  status: "PENDING" | "APPROVED" | "REJECTED" | "EXPIRED" | "WITHDRAWN";
  priorityCode: string;
  requestedAt: string;
  requestedBy: string;
  expiresAt: string;
  decidedAt?: string;
  decidedBy?: string;
  note?: string;
  threadId?: string;
  driverName: string;
  fromDockCode?: string;
  fromStart?: string;
  toSlotId: string;
  toDockCode: string;
  toStart: string;
}

export interface CoordinatorCase {
  caseId: string;
  shipmentId?: string;
  driverName?: string;
  priorityCode?: string;
  threadId?: string;
  exceptionId?: string;
  channel: string;
  phone?: string;
  reasonCode: string;
  urgency: "LOW" | "NORMAL" | "URGENT";
  summary: string;
  status: "OPEN" | "ASSIGNED" | "RESOLVED";
  createdAt: string;
  createdBy: string;
  assignedTo?: string;
}

export interface DriverException {
  exceptionId: string;
  threadId: string;
  shipmentId?: string;
  driverName: string;
  priorityCode?: string;
  type: string;
  reportedAt: string;
  delayMin?: number;
  declaredEta?: string;
  latestAcceptable?: string;
  severity: string;
  status: string;
  description: string;
}

export interface ChatMessage {
  messageId: string;
  senderType: string;
  text: string;
  sentAt: string;
  parsedIntent?: string;
  requiresHumanReview: boolean;
}

export interface DriverCases {
  approvals: ApprovalRequest[];
  cases: CoordinatorCase[];
  exceptions: DriverException[];
  now: string;
}

export interface ReplanResult {
  status: string;
  finalRunId: string;
  attempts: { runId: string; status: string; outcome?: string; written: number; cancelled: number }[];
}

export interface RequestJob {
  jobId: number;
  trucks: number;
  status: "PENDING" | "DONE" | "FAILED";
  result?: string;
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message);
  }
}

/** True for a 401: the session is missing or expired. */
export const isUnauthenticated = (e: unknown) => e instanceof ApiError && e.status === 401;

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: init?.body ? { "Content-Type": "application/json" } : undefined,
  });
  const text = await res.text();
  let body: unknown = undefined;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    // not JSON (e.g. the proxy's own error page)
  }
  if (!res.ok) {
    const b = (body ?? {}) as { error?: string; code?: string };
    throw new ApiError(b.error ?? `request failed (${res.status})`, res.status, b.code);
  }
  return body as T;
}

export const FACILITY = "FAC-JAI-01";
const f = `/api/facilities/${FACILITY}`;

export interface Me {
  email: string;
  name: string;
}

export interface ShipmentRow {
  shipmentId: string;
  priorityCode: string;
  originCity: string;
  requiredDockType: string;
  loadWeightKg: number;
  unloadMin: number;
  status: string;
  driverName: string;
  driverPhone: string;
  createdAt: string;
  eta?: string;
  etaSource?: string;
  gateIn?: string;
  dockCode?: string;
  slotStart?: string;
  bookingStatus?: string;
  planStatus?: string;
  planReason?: string;
}

export interface NewShipment {
  priorityCode: string;
  requiredDockType: string;
  loadWeightKg: number;
  unloadMin: number;
  originCity: string;
  eta: string;
}

export interface CapacitySlot {
  slotId: string;
  dockId: string;
  start: string;
  end: string;
  status: string;
  reason?: string;
  shipmentId?: string;
}

export interface Capacity {
  docks: Dock[];
  events: Block[];
  slots: CapacitySlot[];
}

export interface AgentBooking {
  appointmentId: string;
  slotId: string;
  dockCode: string;
  start: string;
  end: string;
  status: string;
  expiresAt?: string;
}

export interface AgentShipment {
  shipmentId: string;
  driverName: string;
  driverPhone: string;
  priorityCode: string;
  eta?: string;
  etaSource?: string;
  gateIn?: string;
  booking?: AgentBooking;
  hold?: AgentBooking;
  planStatus?: string;
  planReason?: string;
  approvalRequest?: ApprovalRequest;
  message: string;
}

export interface FeedChange {
  changeId: number;
  kind: "BOOKING" | "APPROVAL" | "REPLY";
  shipmentId?: string;
  action: string;
  message: string;
  changedAt: string;
}

const json = (v: unknown) => JSON.stringify(v);

export const api = {
  me: () => call<Me>("/api/auth/me"),
  login: (email: string, password: string) => call<Me>("/api/auth/login", { method: "POST", body: JSON.stringify({ email, password }) }),
  logout: () => call<void>("/api/auth/logout", { method: "POST" }),
  clock: () => call<{ now: string }>("/api/clock"),
  board: () => call<Board>(`${f}/board`),
  exceptions: () => call<Exception[]>(`${f}/exceptions`),
  changes: (limit = 60) => call<Change[]>(`${f}/changes?limit=${limit}`),
  runs: (limit = 30) => call<RunSummary[]>(`${f}/runs?limit=${limit}`),
  replan: () => call<ReplanResult>(`${f}/replan`, { method: "POST" }),
  addTrucks: (trucks: number) => call<RequestJob>(`${f}/truck-requests`, { method: "POST", body: JSON.stringify({ trucks }) }),
  requestJob: (id: number) => call<RequestJob>(`/api/truck-requests/${id}`),

  shipments: () => call<ShipmentRow[]>(`${f}/shipments`),
  createShipment: (s: NewShipment) => call<ShipmentRow>(`${f}/shipments`, { method: "POST", body: json(s) }),
  editShipment: (id: string, edit: Partial<Pick<ShipmentRow, "priorityCode" | "requiredDockType" | "loadWeightKg" | "unloadMin">>) =>
    call<unknown>(`/api/shipments/${encodeURIComponent(id)}`, { method: "PATCH", body: json(edit) }),
  setEta: (id: string, eta: string, source: string, reason?: string) =>
    call<AgentShipment>(`/api/shipments/${encodeURIComponent(id)}/eta`, {
      method: "POST",
      body: json({ eta, source, confidence: "HIGH", reason: reason || undefined }),
    }),
  checkIn: (id: string) => call<AgentShipment>(`/api/shipments/${encodeURIComponent(id)}/checkin`, { method: "POST" }),
  capacity: () => call<Capacity>(`${f}/capacity`),
  addDockEvent: (e: { dockId: string; type: string; start: string; end?: string; reason: string }) =>
    call<unknown>(`${f}/dock-events`, { method: "POST", body: json(e) }),
  setDockStatus: (dockId: string, status: string) =>
    call<unknown>(`${f}/docks/${encodeURIComponent(dockId)}`, { method: "PATCH", body: json({ status }) }),
  setSlotStatus: (slotId: string, status: string, reason?: string) =>
    call<unknown>(`/api/slots/${encodeURIComponent(slotId)}/status`, { method: "POST", body: json({ status, reason }) }),

  // What the WhatsApp driver agent calls (the driver chat console uses the same routes).
  agent: {
    byPhone: (phone: string) => call<AgentShipment[]>(`/api/agent/drivers/${encodeURIComponent(phone)}/shipments`),
    shipment: (id: string) => call<AgentShipment>(`/api/agent/shipments/${encodeURIComponent(id)}`),
    options: (id: string) => call<SlotOption[]>(`/api/agent/shipments/${encodeURIComponent(id)}/options?limit=4`),
    reportEta: (id: string, eta: string, reason: string) =>
      call<AgentShipment>(`/api/agent/shipments/${encodeURIComponent(id)}/eta`, { method: "POST", body: json({ eta, reason }) }),
    hold: (shipmentId: string, slotId: string) => call<Hold>("/api/agent/holds", { method: "POST", body: json({ shipmentId, slotId }) }),
    confirm: (id: string) => call<Hold>(`/api/agent/holds/${encodeURIComponent(id)}/confirm`, { method: "POST" }),
    release: (id: string) => call<Hold>(`/api/agent/holds/${encodeURIComponent(id)}/release`, { method: "POST" }),
    changes: (after: number, limit = 100) =>
      call<{ changes: FeedChange[]; next: number; last: number }>(`/api/agent/changes?after=${after}&limit=${limit}`),
  },
  // The Driver cases screen (coordinators only).
  driverCases: () => call<DriverCases>(`${f}/driver-cases`),
  threadMessages: (threadId: string) => call<ChatMessage[]>(`/api/threads/${encodeURIComponent(threadId)}/messages`),
  approve: (id: string, note?: string) =>
    call<ApprovalRequest>(`/api/approvals/${encodeURIComponent(id)}/approve`, { method: "POST", body: json({ note: note || undefined }) }),
  reject: (id: string, note?: string) =>
    call<ApprovalRequest>(`/api/approvals/${encodeURIComponent(id)}/reject`, { method: "POST", body: json({ note: note || undefined }) }),
  assignCase: (id: string) => call<CoordinatorCase>(`/api/cases/${encodeURIComponent(id)}/assign`, { method: "POST" }),
  resolveCase: (id: string, reply?: string) =>
    call<CoordinatorCase>(`/api/cases/${encodeURIComponent(id)}/resolve`, { method: "POST", body: json({ reply: reply || undefined }) }),

  options: (shipmentId: string) => call<SlotOption[]>(`/api/shipments/${encodeURIComponent(shipmentId)}/options?limit=8`),
  hold: (shipmentId: string, slotId: string) => call<Hold>("/api/holds", { method: "POST", body: JSON.stringify({ shipmentId, slotId }) }),
  confirm: (id: string) => call<Hold>(`/api/holds/${encodeURIComponent(id)}/confirm`, { method: "POST" }),
  release: (id: string) => call<Hold>(`/api/holds/${encodeURIComponent(id)}/release`, { method: "POST" }),
};
