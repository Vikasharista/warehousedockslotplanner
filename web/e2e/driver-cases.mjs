// Browser test for the Driver cases screen (phase 7). Needs the local stack on a fresh database
// (docker compose down -v && docker compose up --build) and Playwright with Chromium:
//
//   node web/e2e/driver-cases.mjs [screenshot-dir]
//
// Two priority trucks ask for another slot through the agent API (SHP1009 CRITICAL, SHP1010 HIGH); both changes wait
// for approval. On the screen a coordinator approves one, rejects the other with a note and answers an escalated case.
// The script then reads the driver feed: one APPROVED, one REJECTED and one REPLY notice. Exits non-zero on failure.
import { createRequire } from "node:module";
const { chromium } = createRequire(import.meta.url)("playwright");

const API = process.env.DOCK_API_URL ?? "http://localhost:8082";
const WEB = process.env.WEB_URL ?? "http://localhost:5173";
const KEY = { "X-Agent-Key": process.env.AGENT_KEY ?? "local-agent-key-for-development-only", "Content-Type": "application/json" };
const out = process.argv[2] ?? ".";

async function agent(method, path, body) {
  const r = await fetch(API + "/api/agent" + path, { method, headers: KEY, body: body ? JSON.stringify(body) : undefined });
  return { status: r.status, body: await r.json() };
}

async function requestChange(shipment) {
  const opts = (await agent("GET", `/shipments/${shipment}/options?limit=1`)).body;
  const hold = await agent("POST", "/holds", { shipmentId: shipment, slotId: opts[0].slotId });
  if (hold.status !== 201) throw new Error(`hold ${shipment}: ${JSON.stringify(hold.body)}`);
  const c = await agent("POST", `/holds/${hold.body.appointmentId}/confirm`);
  if (c.status !== 202) throw new Error(`confirm ${shipment} answered ${c.status}`);
  return c.body.approvalRequest;
}

const approveReq = await requestChange("SHP1009"); // CRITICAL
const rejectReq = await requestChange("SHP1010"); // HIGH (reefer)
// An escalated case with a conversation, so the screen shows all three lists.
const thread = (await agent("POST", "/threads", { shipmentId: "SHP1005" })).body;
await agent("POST", `/threads/${thread.threadId}/messages`, { senderType: "DRIVER", text: "Can I unload earlier?" });
await agent("POST", "/cases", { reasonCode: "GENERAL_QUESTION", urgency: "NORMAL", summary: "Driver asks about unloading earlier",
  shipmentId: "SHP1005", threadId: thread.threadId, channel: "TELEGRAM", channelUserId: "4242" });

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 1000 }, deviceScaleFactor: 2 });
await page.goto(WEB);
await page.getByLabel("Email").fill("demo@setuhaul.local");
await page.getByLabel("Password").fill("setuhaul-demo-2026");
await page.getByRole("button", { name: /sign in/i }).click();
await page.getByRole("link", { name: "Driver cases" }).or(page.getByRole("button", { name: "Driver cases" })).first().click();
await page.locator(`[data-request="${approveReq.requestId}"]`).waitFor();
await page.locator(`[data-request="${rejectReq.requestId}"]`).waitFor();
await page.getByRole("button", { name: "Open conversation" }).first().click();
await page.getByLabel(/^Conversation/).waitFor();
await page.screenshot({ path: `${out}/p7-driver-cases.png`, fullPage: true });

await page.locator(`[data-request="${approveReq.requestId}"]`).getByRole("button", { name: "Approve" }).click();
await page.getByText(/Approved: SHP1009/).waitFor();
const rejectItem = page.locator(`[data-request="${rejectReq.requestId}"]`);
await rejectItem.getByPlaceholder(/Note to the driver/).fill("D5 is needed for another cold load");
await rejectItem.getByRole("button", { name: "Reject" }).click();
await page.getByText(/Rejected: SHP1010/).waitFor();
await page.getByText("Nothing waiting.").waitFor();
const caseItem = page.locator("[data-case]").first();
await caseItem.getByPlaceholder(/Reply to the driver/).fill("Yes, come at 11:00 to dock D2.");
await caseItem.getByRole("button", { name: "Resolve" }).click();
await page.getByText("No open cases.").waitFor();
await page.screenshot({ path: `${out}/p7-driver-cases-after.png`, fullPage: true });
await browser.close();

const feed = (await agent("GET", "/changes?after=0&limit=500")).body.changes.filter((c) => c.kind !== "BOOKING");
const actions = feed.map((c) => c.action).join(",");
console.log(feed.map((c) => c.message).join("\n"));
if (actions !== "APPROVED,REJECTED,REPLY") {
  console.error(`expected APPROVED,REJECTED,REPLY on the feed, got ${actions}`);
  process.exit(1);
}
console.log("driver cases: OK");
