# The scenario run and the evals: python evals/scenario_run.py from the repository root, on the stack's network.
# Build from the repository root: docker build -f evals/Dockerfile -t setuhaul/evals .
FROM python:3.12-slim
COPY evals/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt
WORKDIR /repo
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
