# Deploying the reviewer demo

The whole planner runs on one Linux VM with Docker, the same stack as on your laptop. Caddy sits in front and serves
HTTPS. Reviewers sign in with the demo accounts you set. Every night at 03:00 IST the demo resets to the snapshot's
4 Aug 2026 09:55 with a fresh 70-truck load test; accounts are kept.

## What you need

| Item | Minimum |
| --- | --- |
| VM | Any Linux VM with 2 vCPUs, 4 GB RAM (2 GB works but is tight) and 20 GB disk; x86-64 or ARM |
| Software | Docker Engine with Docker Compose v2.24 or later, and git |
| Network | Inbound TCP 80 and 443 open to the internet (cloud firewall and the VM's own) |
| Hostname | A name that points at the VM's public IP: your own domain, or the free `<ip-with-dashes>.sslip.io` (e.g. `203-0-113-7.sslip.io`) |

## Steps

1. Create the VM, open ports 80 and 443, and install Docker (`curl -fsSL https://get.docker.com | sh` on most
   distributions).
2. Get the code:

   ```bash
   git clone https://github.com/Vikasharista/warehousedockslotplanner.git
   cd warehousedockslotplanner
   ```

3. Write the settings. `.env` holds secrets and is ignored by git:

   ```bash
   cp deploy.env.example .env
   ```

   Fill in every value. `openssl rand -hex 16` makes a database password, and `openssl rand -hex 32` makes a session
   secret. Demo accounts are `email:password` pairs separated by commas, for example
   `reviewer@university.edu:a-long-password,you@example.com:another-long-password`.

4. Start everything:

   ```bash
   docker compose -f docker-compose.yml -f docker-compose.deploy.yml up -d --build
   ```

   The first build takes several minutes; the solver step prints Maven's download progress. Caddy fetches the HTTPS
   certificate on the first request to your hostname.

5. Open `https://<your hostname>`, sign in, and give reviewers the URL plus their email and password.

## Running it

| Task | Command |
| --- | --- |
| Status | `docker compose -f docker-compose.yml -f docker-compose.deploy.yml ps` |
| Logs | `docker compose -f docker-compose.yml -f docker-compose.deploy.yml logs -f solver` (or `api`, `caddy`, `reset`) |
| Reset the demo now | `docker compose -f docker-compose.yml -f docker-compose.deploy.yml run --rm --no-deps reset python -m setuhaul_jobs.demo_reset --now` |
| Add, remove or change an account | Edit `SETUHAUL_DEMO_ACCOUNTS` in `.env`, then rerun the `up -d` command from step 4. Removing an entry does not delete an existing account; restart with a new password instead |
| Update to new code | `git pull`, then rerun the `up -d --build` command |
| Stop | `docker compose -f docker-compose.yml -f docker-compose.deploy.yml down` (add `-v` to delete the data too) |

## How it is protected

- **Open ports:** only Caddy listens on the internet (80 redirects to 443). Postgres, the solver and dock-api have no
  public ports.
- **Secrets:** the database password, the session secret and the accounts come from `.env`. The deployment refuses to
  start without them, and there are no defaults.
- **Sign-in:** passwords are stored as bcrypt hashes. A session is a signed cookie (HttpOnly, Secure,
  SameSite=Strict) valid for 12 hours. Five failed sign-ins for one email, or from one address, pause sign-in for
  15 minutes.
- **Every API route needs a session**, except the health check and sign-in itself.
- **Not covered:** password reset by email, or accounts managed in the app; it is a demo.
