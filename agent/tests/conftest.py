"""Live tests run the agent against a running planner on the real snapshot (docker compose up on a fresh database:
docker compose down -v first). PLANNER_URL (default http://localhost:8082) and PLANNER_AGENT_KEY pick it; without a
planner the live tests are skipped and only the unit tests run."""
import os

import httpx
import pytest

from setuhaul_agent.agent import Agent
from setuhaul_agent.channels.sim import SimDriver
from setuhaul_agent.nlu import RuleNLU
from setuhaul_agent.planner import Planner
from setuhaul_agent.store import Store

PLANNER_URL = os.environ.get("PLANNER_URL", "http://localhost:8082")
AGENT_KEY = os.environ.get("PLANNER_AGENT_KEY", "local-agent-key-for-development-only")
DEMO = (os.environ.get("DEMO_EMAIL", "demo@setuhaul.local"), os.environ.get("DEMO_PASSWORD", "setuhaul-demo-2026"))


def phone(n: int) -> str:
    """The snapshot's driver phones: SHP10nn's driver is +91-90000100nn (a few drivers have two trips)."""
    return f"+91-90000100{n:02d}"


@pytest.fixture(scope="session")
def planner():
    try:
        httpx.get(PLANNER_URL + "/api/health", timeout=3).raise_for_status()
    except httpx.HTTPError:
        pytest.skip(f"no planner at {PLANNER_URL}")
    p = Planner(PLANNER_URL, AGENT_KEY)
    yield p
    p.close()


@pytest.fixture
def store():
    s = Store(":memory:")
    yield s
    s.close()


@pytest.fixture
def agent(planner, store):
    return Agent(planner, store, RuleNLU())


@pytest.fixture
def driver(agent):
    def make(n: int, name: str | None = None, **kw) -> SimDriver:
        return SimDriver(agent, phone(n), name or f"driver {n}", **kw)
    return make


@pytest.fixture(scope="session")
def coordinator(planner):
    """A signed-in plan-view session (the Driver cases screen's routes)."""
    c = httpx.Client(base_url=PLANNER_URL, timeout=30)
    c.post("/api/auth/login", json={"email": DEMO[0], "password": DEMO[1]}).raise_for_status()
    yield c
    c.close()

