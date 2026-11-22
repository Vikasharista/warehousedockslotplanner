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

export const api = {
  me: () => call<Me>("/api/auth/me"),
  login: (email: string, password: string) =>
    call<Me>("/api/auth/login", { method: "POST", body: JSON.stringify({ email, password }) }),
  logout: () => call<void>("/api/auth/logout", { method: "POST" }),
  clock: () => call<{ now: string }>("/api/clock"),
  board: () => call<Board>(`${f}/board`),
  exceptions: () => call<Exception[]>(`${f}/exceptions`),
  changes: (limit = 60) => call<Change[]>(`${f}/changes?limit=${limit}`),
  runs: (limit = 30) => call<RunSummary[]>(`${f}/runs?limit=${limit}`),
  replan: () => call<ReplanResult>(`${f}/replan`, { method: "POST" }),
  addTrucks: (trucks: number) =>
    call<RequestJob>(`${f}/truck-requests`, { method: "POST", body: JSON.stringify({ trucks }) }),
  requestJob: (id: number) => call<RequestJob>(`/api/truck-requests/${id}`),
  options: (shipmentId: string) => call<SlotOption[]>(`/api/shipments/${encodeURIComponent(shipmentId)}/options?limit=8`),
  hold: (shipmentId: string, slotId: string) =>
    call<Hold>("/api/holds", { method: "POST", body: JSON.stringify({ shipmentId, slotId }) }),
  confirm: (id: string) => call<Hold>(`/api/holds/${encodeURIComponent(id)}/confirm`, { method: "POST" }),
  release: (id: string) => call<Hold>(`/api/holds/${encodeURIComponent(id)}/release`, { method: "POST" }),
};
