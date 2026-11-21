# Seed loader and live GPS feed. Build from the repository root: docker build -f jobs/Dockerfile .
FROM python:3.12-slim
WORKDIR /app
COPY jobs/requirements.txt jobs/requirements.txt
RUN pip install --no-cache-dir -r jobs/requirements.txt
# The jobs read the snapshot, the routes and the SQL from the repository layout.
COPY data data
COPY db db
COPY jobs jobs
WORKDIR /app/jobs
ENV PYTHONUNBUFFERED=1
CMD ["python", "-m", "setuhaul_jobs.seed", "--init"]
