# The driver chat agent. Build from agent/: docker build agent
FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt requirements.txt
RUN pip install --no-cache-dir -r requirements.txt
COPY setuhaul_agent setuhaul_agent
# Its own state (sessions, handled messages, the feed cursor) lives on the /state volume; nothing else is written.
RUN useradd --system --uid 10001 agent && mkdir /state && chown agent /state
USER agent
ENV PYTHONUNBUFFERED=1 AGENT_STATE_DB=/state/agent-state.db
CMD ["python", "-m", "setuhaul_agent"]
