# web

The plan view: React 18, TypeScript and Vite.

- **Dock sequence.** One row per dock over the planning window: bookings coloured by priority with predicted wait,
  driver holds dashed, flagged bookings outlined, blocked time hatched, and a red line at the shared clock's now.
  Click a booking for its details.
- **Side panel.** Exceptions (no safe slot, coordinator, in dock), the change log, the runs, and driver holds (find
  a free slot for a truck, hold it, confirm or release).
- **Re-plan now.** Asks the solver to plan and commit at once.

Times are shown in IST whatever your own time zone is. The page polls every 10 seconds.

```bash
npm install
npm run dev          # http://localhost:5173, /api proxied to dock-api on localhost:8082 (DOCK_API_URL to change)
npm test             # unit tests
npm run build        # type-check and build into dist/
```

In Docker Compose the built page is served by nginx on port 5173, which also forwards `/api` to dock-api.
