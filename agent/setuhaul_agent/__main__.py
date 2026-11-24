"""Run the driver agent: python -m setuhaul_agent

Settings come from the environment (see agent/README.md): PLANNER_URL, PLANNER_AGENT_KEY, AGENT_STATE_DB,
TELEGRAM_TOKEN (Telegram), WHATSAPP_* (WhatsApp, optional), FEED_EVERY_SECONDS, ALLOW_PHONE_OVERRIDE, and
ANTHROPIC_API_KEY to read free text with Claude (rules otherwise). With neither channel configured the agent only
logs that it has nothing to do.
"""
from __future__ import annotations

import logging
import threading
import time
from http.server import ThreadingHTTPServer

from .agent import Agent
from .config import Settings
from .notices import deliver
from .planner import Planner, PlannerError, PlannerUnavailable
from .store import Store

log = logging.getLogger("setuhaul_agent")


def feed_loop(planner: Planner, store: Store, senders: dict, every: float, stop: threading.Event) -> None:
    while not stop.is_set():
        try:
            n = deliver(planner, store, senders)
            if n:
                log.info("sent %d plan-change notices", n)
        except PlannerError as e:
            if e.status == 401:
                log.critical("the planner refused the agent key: check PLANNER_AGENT_KEY")
            else:
                log.warning("change feed: %s", e)
        except PlannerUnavailable as e:
            log.warning("change feed: planner unavailable (%s)", e)
        except Exception:
            log.exception("change feed")
        stop.wait(every)


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    cfg = Settings.from_env()
    planner = Planner(cfg.planner_url, cfg.agent_key)
    store = Store(cfg.state_db)
    agent = Agent(planner, store)
    log.info("message reading: %s", agent.nlu.name)
    senders: dict = {}
    threads: list[threading.Thread] = []
    stop = threading.Event()

    if cfg.telegram_token:
        from .channels.telegram import BotApi, TelegramBot

        bot = TelegramBot(BotApi(cfg.telegram_token), agent, cfg.allow_phone_override)
        senders["TELEGRAM"] = bot.deliver
        threads.append(threading.Thread(target=bot.run, name="telegram", daemon=True))
    if cfg.whatsapp:
        from .channels.whatsapp import WhatsAppClient, make_handler

        wa = cfg.whatsapp
        client = WhatsAppClient(wa["whatsapp_token"], wa["whatsapp_phone_number_id"])
        senders["WHATSAPP"] = lambda chat_id, msg: client.send(msg, chat_id)
        server = ThreadingHTTPServer(("0.0.0.0", wa["port"]),
                                     make_handler(agent, client, wa["whatsapp_app_secret"], wa["whatsapp_verify_token"]))
        threads.append(threading.Thread(target=server.serve_forever, name="whatsapp", daemon=True))
    if not threads:
        log.warning("no channel configured: set TELEGRAM_TOKEN (or WHATSAPP_*); the agent is idle")
        while True:
            time.sleep(3600)
    threads.append(threading.Thread(target=feed_loop, args=(planner, store, senders, cfg.feed_every_seconds, stop),
                                    name="feed", daemon=True))
    for t in threads:
        t.start()
    try:
        while all(t.is_alive() for t in threads):
            time.sleep(1)
    finally:
        stop.set()


if __name__ == "__main__":
    main()
