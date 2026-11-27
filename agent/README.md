# Driver chat agent

Talks to truck drivers on Telegram (and WhatsApp, once the demo has a public HTTPS address):
- finds their trip from their phone number;
- takes delay, breakdown and early-arrival reports;
- offers and holds another slot;
- tells them when the plan changes.

It started as the team's `setuhaul-agent` prototype and was moved here in phase 7 ([plan](https://claude.ai/artifact/CEhBRBoLMYFSsm8Yce24ny)).

## How it fits

- The agent owns the conversation: the state machine (`agent.py`), message reading (`nlu.py`), phone verification and
  the channel adapters (`channels/`).
- The planner owns everything else. The agent reaches it only through dock-api's `/api/agent/*` routes
  ([docs/agent-api.md](../docs/agent-api.md)) with its agent key (`planner.py`). That covers trips, ETAs, slot options,
  holds, chat threads and messages, driver exceptions and coordinator cases.
- The agent has no SQL against planner data and no clock of its own: every time is the planner's (a trip's `asOf`).
  Every slot comes from the planner's options, which follow the solver's rules.
- Its own state is a private SQLite file (`store.py`, `AGENT_STATE_DB`) that nothing else reads. It holds sessions,
  the inbound messages already handled, and the change-feed cursor.

What a driver can do:

| Driver says | Agent does |
| --- | --- |
| shares their number | finds the open trip(s), asks which one; an unknown number becomes a coordinator case |
| "where is my slot?" | the planner's status message, the ETA the plan uses and its source, the gate-by time |
| "1 hour late, traffic" | records the exception and the ETA, then answers with the ETA the plan uses (GPS may win): the slot still works (the planner's rule), or offers others |
| "can I get another slot?" | up to 3 of the planner's options, within the driver's own limits ("after 7", "finish by 21:00") |
| picks a slot | a 15-minute hold, then confirm or release |
| confirms (LOW, NORMAL) | the booking changes at once |
| confirms (HIGH, CRITICAL) | 202 from the planner: the old booking stands and the new slot is reserved for 60 minutes until a coordinator decides on the **Driver cases** screen; the answer comes back on the feed |
| "already reached early" | reports the ETA only; the planner moves an early truck itself when an earlier slot is free |
| asks for a slot once the truck is in its dock | the trip status: the slot can no longer change by chat (the planner's `canReschedule`) |
| accident, injury, robbery | an URGENT case and "call 112" |
| asks for a person, or the agent cannot help | a case for a coordinator; their reply comes back here. A conversation keeps one open case: a later hand-over (a safety message, say) is added to it and raises its urgency |

Changes to a truck's booking reach its driver from the change feed every 30 seconds:
- the solver moving or releasing a booking;
- a coordinator's approval or rejection, or an approval that expired;
- a coordinator's reply to a case.

## Settings (environment)

| Variable | Default | Meaning |
| --- | --- | --- |
| `PLANNER_URL` | `http://localhost:8082` | dock-api (`http://api:8082` in Compose) |
| `PLANNER_AGENT_KEY` | the local development key | must be one of dock-api's `APP_AGENT_KEYS` |
| `TELEGRAM_TOKEN` | unset | the bot token from @BotFather; unset means Telegram is off |
| `TELEGRAM_WORKERS` | `8` | Telegram updates handled at once, one driver at a time each: a slow reply for one driver does not hold up the others. `0` handles every update on the polling thread |
| `ALLOW_PHONE_OVERRIDE` | `false` | `true` enables `/as <phone>` to act as a seeded driver (local testing only) |
| `ANTHROPIC_API_KEY` | unset | read free text with Claude (10-second timeout, one retry, then rules); unset means rules only |
| `SETUHAUL_LLM_MODEL` | `claude-sonnet-5-5` | the model for message reading |
| `AGENT_STATE_DB` | `agent-state.db` (`/state/...` in Compose) | the private state file |
| `FEED_EVERY_SECONDS` | `30` | how often plan changes are read |
| `WHATSAPP_TOKEN`, `WHATSAPP_PHONE_NUMBER_ID`, `WHATSAPP_APP_SECRET`, `WHATSAPP_VERIFY_TOKEN`, `WHATSAPP_PORT` | unset | WhatsApp Cloud API webhook (needs a public HTTPS URL) |

## Tests

```bash
cd agent && python -m venv .venv && .venv/bin/pip install -r requirements-dev.txt
```

```bash
.venv/bin/python -m pytest
```

There are two kinds of test:
- **Unit tests** need nothing running. They cover message reading and its range checks, and the Telegram bot:
  failures, retries and group chats, against a stubbed Telegram. They also cover the buttons, WhatsApp parsing, the
  private store, and a check that no file touches planner tables.
- **Live tests** run the agent against the planner on the real snapshot and are skipped when no planner answers. They
  need a fresh database, because they change bookings:

```bash
docker compose down -v && docker compose up --build -d
```

## Try it live on Telegram (your laptop)

The bot token is a password: keep it in `.env` (git-ignored), never in chat or in the repository. Telegram lets only
one program read a bot's messages at a time, so use a bot of your own. Your teammate and the deployed demo each need
their own bot.

1. In Telegram, open **@BotFather** (blue tick) and send `/newbot`. Give it a name (*SetuHaul Dock Assistant*) and a
   username ending in `bot`. Copy the token it gives you.
2. Still in BotFather: `/setjoingroups` → Disable (the agent answers only private chats anyway).
   Then `/setcommands` → `start - Share your phone number to find your trip`.
3. In the repository root, create or edit `.env`:

   ```
   TELEGRAM_TOKEN=123456789:AA...
   ALLOW_PHONE_OVERRIDE=true
   ```

4. Start everything on a fresh database:

```bash
docker compose down -v
```

```bash
docker compose up --build
```

5. Check that the agent started: `docker compose logs agent` should show `Telegram polling started`.

Then walk through this checklist in Telegram:

| # | Do | Expect |
| --- | --- | --- |
| 1 | Open your bot, send `/start` | A greeting and a **Share my phone number** button |
| 2 | Send `/as +91-9000010009` (your own number is not in the demo data) | "Is this your current trip?" for SHP1009 (Ahmedabad) |
| 3 | Tap **Yes** | "Thanks, trip confirmed" and the menu |
| 4 | Type `where is my slot?` | Dock and time, the arrival time the plan uses (GPS) and the gate-by time |
| 5 | Type `stuck in traffic, 40 min late` | Either "still works" or "will not work" with a question about your latest finish |
| 6 | Type `can I get another slot?`, answer `no limit` | Up to 3 slots as buttons |
| 7 | Tap a slot | "Holding ... until HH:MM" (15 minutes), Yes / No |
| 8 | Tap **Yes** | SHP1009 is CRITICAL, so: "a coordinator approves the change", your old slot stands |
| 9 | Sign in at http://localhost:5173 (demo@setuhaul.local / setuhaul-demo-2026), open **Driver cases**, approve it | Within 30 seconds Telegram says "SHP1009: your change is approved..." |
| 10 | `/as +91-9000010003`, Yes on the trip, then ask for another slot, tap one, Yes | SHP1003 is LOW: "Confirmed" at once |
| 11 | On the **Capacity** screen, block SHP1003's new slot | Within a minute or so Telegram says it moved or lost its slot |
| 12 | Type `accident, my helper is hurt` | "call 112", a case on **Driver cases**; resolve it with a reply and the reply arrives in Telegram |
| 13 | Add the bot to a group and write there | No reply |
| 14 | `docker compose stop api`, send a message, `docker compose start api` within a minute | The bot keeps running, and your message is answered once the planner is back. Each message is tried 3 times, one poll apart, about a minute in all |

If something is off, `docker compose logs agent api` shows each call and refusal.
