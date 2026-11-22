"""Repository paths shared by the jobs."""

from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
SNAPSHOT_DB = REPO_ROOT / "data" / "setuhaul" / "setuhaul_freight_operations.db"
SCHEMA_SQL = REPO_ROOT / "db" / "schema.sql"
PLAN_TABLES_SQL = REPO_ROOT / "db" / "plan_tables.sql"
GPS_TABLES_SQL = REPO_ROOT / "db" / "gps_tables.sql"
COMMIT_TABLES_SQL = REPO_ROOT / "db" / "commit_tables.sql"
AUTH_TABLES_SQL = REPO_ROOT / "db" / "auth_tables.sql"
AGENT_TABLES_SQL = REPO_ROOT / "db" / "agent_tables.sql"
VALIDATE_DIR = REPO_ROOT / "db" / "validate"
AUDIT_DIR = REPO_ROOT / "db" / "audit"
AUDIT_REPORT = REPO_ROOT / "docs" / "data-audit.md"
