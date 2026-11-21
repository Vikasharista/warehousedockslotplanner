"""Test fixtures: a throwaway Postgres database, schema applied and seeded from the snapshot.

Set TEST_DATABASE_URL to a Postgres server where the user may create databases, for example the
docker-compose service:  TEST_DATABASE_URL=postgresql://postgres:postgres@localhost:55432/postgres
Each test session creates its own database and drops it at the end.
"""

from __future__ import annotations

import os
import uuid
from collections.abc import Iterator

import psycopg
import pytest
from psycopg import sql
from psycopg.conninfo import conninfo_to_dict, make_conninfo

from setuhaul_jobs import seed


@pytest.fixture(scope="session")
def admin_url() -> str:
    url = os.environ.get("TEST_DATABASE_URL")
    if not url:
        pytest.skip("TEST_DATABASE_URL is not set; see jobs/README.md")
    return url


@pytest.fixture(scope="session")
def database_url(admin_url: str) -> Iterator[str]:
    name = f"setuhaul_test_{uuid.uuid4().hex[:8]}"
    with psycopg.connect(admin_url, autocommit=True) as admin:
        admin.execute(sql.SQL("CREATE DATABASE {}").format(sql.Identifier(name)))
    params = conninfo_to_dict(admin_url)
    params["dbname"] = name
    url = make_conninfo(**params)
    try:
        with psycopg.connect(url) as conn:
            seed.apply_schema(conn)
            conn.commit()
        yield url
    finally:
        with psycopg.connect(admin_url, autocommit=True) as admin:
            admin.execute(sql.SQL("DROP DATABASE IF EXISTS {} WITH (FORCE)").format(sql.Identifier(name)))


@pytest.fixture(scope="session")
def snapshot():
    conn = seed.open_snapshot()
    yield conn
    conn.close()


@pytest.fixture
def seeded(database_url: str, snapshot) -> Iterator[psycopg.Connection]:
    """A connection to a freshly seeded database. Tests may write; the next test reseeds."""
    with psycopg.connect(database_url) as conn:
        seed.load(conn, snapshot)
        conn.execute(f"SET TIME ZONE '{seed.SNAPSHOT_TIMEZONE}'")
        conn.commit()
        yield conn
        conn.rollback()
