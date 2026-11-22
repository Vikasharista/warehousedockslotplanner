"""Generate extra truck requests to load-test slot allocation.

    python -m setuhaul_jobs.scenario [--trucks 70] [--trickle 20] [--seed 42] [--once]
        [--no-disruptions] [--coordinator-cases 4] [--facility FAC-JAI-01]

Writes to Postgres only (never to the SQLite snapshot): a driver, a vehicle and a shipment per truck, plus ETA
updates and one dock event for the disruptions. `docker compose down -v` returns to the plain snapshot.

- `--trucks` in total, of which `--trickle` are requested during the day (10:00 to 17:00 simulated); the rest are
  known from the clock's now. A request's `created_at` is when it was made: every reader ignores a shipment until
  the shared clock reaches it, so trickle requests appear over the day and fire the NEW_REQUEST trigger.
- The mix follows the snapshot: about 15% reefer and 15% heavy loads, LOW/NORMAL/HIGH/CRITICAL priorities,
  origins on and off the three GPS lanes.
- Disruptions (default on): about a quarter of the trucks later report a delay (or an early arrival), and dock D4
  breaks down for 90 minutes in the afternoon.
- Coordinator cases: weight breaches (a heavy load booked on a lighter dock) and trucks that cannot be at the dock
  before the last start time.
- The same seed always gives the same trucks. `--once` does nothing if generated trucks already exist.
"""

from __future__ import annotations

import argparse
import datetime as dt
import os
import random
import sys
from dataclasses import dataclass, field

import psycopg

FACILITY = "FAC-JAI-01"
ZONE = dt.timezone(dt.timedelta(hours=5, minutes=30))
PREFIX = "G"  # generated ids: SHP-G0001, VEH-G0001, DRV-G0001, ...

# Origin, approximate road time to Jaipur in minutes. The first seven sit on the GPS lanes.
ORIGINS: list[tuple[str, int, float]] = [
    ("Delhi", 330, 3), ("Manesar", 270, 2), ("Neemrana", 180, 2), ("Ajmer", 150, 2), ("Udaipur", 450, 1),
    ("Ahmedabad", 720, 1), ("Mumbai", 1260, 0.5),
    ("Dausa", 75, 2), ("Kota", 280, 1.5), ("Jodhpur", 390, 1), ("Jhunjhunu", 210, 1), ("Bhiwadi", 230, 1.5),
    ("Ludhiana", 620, 0.5),
]
PRIORITIES = [("LOW", 0.20), ("NORMAL", 0.50), ("HIGH", 0.22), ("CRITICAL", 0.08)]
CUSTOMERS = ["HomeCraft Retail", "AgriServe Jaipur", "RajRetail Distribution", "BuildPro Rajasthan",
             "CareSupply Rajasthan", "SetuHaul Jaipur DC"]
STANDARD_GOODS = ["FMCG", "Consumer electronics", "Home appliances", "Textiles", "Packaging material",
                  "Office supplies", "Auto components", "Furniture accessories", "Medical devices"]
REEFER_GOODS = ["Frozen foods", "Dairy products", "Hospital consumables"]
HEAVY_GOODS = ["Industrial machinery", "Construction equipment", "Machine parts", "Solar equipment"]
DELAY_REASONS = ["TRAFFIC", "TRAFFIC", "LOADING_DELAY", "WEATHER", "BREAKDOWN", "LATE_DEPARTURE"]
FIRST_NAMES = ["Ravi", "Suresh", "Mahesh", "Imran", "Harpreet", "Deepak", "Arjun", "Vikram", "Sanjay", "Naveen",
               "Rakesh", "Gurpreet", "Anil", "Manoj", "Sunil", "Ajay"]
LAST_NAMES = ["Sharma", "Singh", "Yadav", "Khan", "Meena", "Choudhary", "Patel", "Gill", "Rathore", "Verma"]

LAST_ARRIVAL = dt.time(20, 30)  # latest regular ETA; later ones are the coordinator cases


@dataclass
class Truck:
    shipment_id: str
    vehicle_id: str
    driver_id: str
    carrier_id: str
    vehicle_type: str
    capacity_kg: int
    refrigerated: bool
    origin: str
    customer: str
    product: str
    load_kg: int
    pallets: int
    dock_type: str
    temperature: bool
    priority: str
    unload_min: int
    created_at: dt.datetime
    departure: dt.datetime
    eta: dt.datetime
    driver_name: str
    eta_updates: list[tuple[dt.datetime, dt.datetime, str, str | None]] = field(default_factory=list)
    booking_dock: str | None = None  # coordinator case: booked on this (too light) dock


@dataclass
class Summary:
    created: list[str]
    at_start: int
    trickle: int
    eta_updates: int
    dock_events: int
    weight_breaches: int
    late_arrivals: int

    def __str__(self) -> str:
        return (f"{len(self.created)} truck requests ({self.at_start} known now, {self.trickle} during the day), "
                f"{self.eta_updates} ETA updates, {self.dock_events} dock events, "
                f"{self.weight_breaches} weight breaches, {self.late_arrivals} late arrivals")


def _pick(rng: random.Random, weighted: list[tuple]) -> tuple:
    total = sum(w[-1] for w in weighted)
    r = rng.uniform(0, total)
    for item in weighted:
        r -= item[-1]
        if r <= 0:
            return item
    return weighted[-1]


def _round5(minutes: float) -> int:
    return max(5, int(round(minutes / 5.0)) * 5)


def _local(day: dt.date, t: dt.time) -> dt.datetime:
    return dt.datetime.combine(day, t, tzinfo=ZONE)


class Generator:
    def __init__(self, conn: psycopg.Connection, facility: str, seed: int):
        self.conn = conn
        self.facility = facility
        self.rng = random.Random(seed)
        self.carriers = [r[0] for r in conn.execute("SELECT carrier_id FROM carriers ORDER BY carrier_id")]
        if not self.carriers:
            raise ValueError("no carriers: seed the snapshot first")
        (n,) = conn.execute(
            "SELECT count(*) FROM shipments WHERE shipment_id LIKE %s", (f"SHP-{PREFIX}%",)).fetchone()
        self.next_number = n + 1

    def _ids(self) -> tuple[str, str, str]:
        n = self.next_number
        self.next_number += 1
        return f"SHP-{PREFIX}{n:04d}", f"VEH-{PREFIX}{n:04d}", f"DRV-{PREFIX}{n:04d}"

    def truck(self, created_at: dt.datetime, eta: dt.datetime, *, kind: str | None = None) -> Truck:
        rng = self.rng
        shipment_id, vehicle_id, driver_id = self._ids()
        roll = rng.random()
        kind = kind or ("REEFER" if roll < 0.15 else "HEAVY" if roll < 0.30 else "STANDARD")
        if kind == "REEFER":
            vtype, cap, refr, load, dock, temp = "REEFER_32", 18000, True, rng.randint(7000, 16500), "REEFER", True
            product, unload = rng.choice(REEFER_GOODS), _round5(rng.uniform(45, 75))
        elif kind == "HEAVY":
            vtype, cap, refr, load, dock, temp = "HEAVY_40", 32000, False, rng.randint(25500, 31000), "HEAVY", False
            product, unload = rng.choice(HEAVY_GOODS), _round5(rng.uniform(75, 110))
        else:
            vtype, cap = _pick(rng, [("20FT", 9000, 3), ("32FT_SXL", 15000, 4), ("32FT_MXL", 22000, 3)])[:2]
            load = rng.randint(int(cap * 0.45), int(cap * 0.97))
            refr, dock, temp = False, "ANY", False
            product = rng.choice(STANDARD_GOODS)
            unload = _round5({"20FT": 35, "32FT_SXL": 50, "32FT_MXL": 65}[vtype] + rng.uniform(-10, 10))
        origin, travel, _ = _pick(rng, ORIGINS)
        departure = eta - dt.timedelta(minutes=travel + rng.randint(-20, 20))
        return Truck(
            shipment_id=shipment_id, vehicle_id=vehicle_id, driver_id=driver_id,
            carrier_id=rng.choice(self.carriers), vehicle_type=vtype, capacity_kg=cap, refrigerated=refr,
            origin=origin, customer=rng.choice(CUSTOMERS), product=product, load_kg=load,
            pallets=max(1, load // 900), dock_type=dock, temperature=temp,
            priority=_pick(rng, PRIORITIES)[0], unload_min=unload, created_at=created_at,
            departure=departure, eta=eta,
            driver_name=f"{rng.choice(FIRST_NAMES)} {rng.choice(LAST_NAMES)}",
        )

    def eta_between(self, earliest: dt.datetime, latest: dt.datetime) -> dt.datetime:
        if latest <= earliest:
            return earliest
        minutes = self.rng.uniform(0, (latest - earliest).total_seconds() / 60)
        return (earliest + dt.timedelta(minutes=minutes)).replace(second=0, microsecond=0)

    def disrupt(self, t: Truck, not_before: dt.datetime) -> None:
        """A later ETA report, made after `not_before` (and the request): usually a delay, sometimes early."""
        rng = self.rng
        start = max(t.created_at, not_before)
        window = (t.eta - start).total_seconds() / 60
        if window < 90:
            return
        reported = start + dt.timedelta(minutes=rng.uniform(15, window - 45))
        if rng.random() < 0.8:
            new_eta = t.eta + dt.timedelta(minutes=rng.randint(40, 150))
            reason = rng.choice(DELAY_REASONS)
        else:
            new_eta = max(reported + dt.timedelta(minutes=30), t.eta - dt.timedelta(minutes=rng.randint(35, 70)))
            reason = None
        t.eta_updates.append((reported.replace(microsecond=0), new_eta.replace(second=0, microsecond=0),
                              rng.choice(["MEDIUM", "HIGH"]), reason))

    def insert(self, trucks: list[Truck]) -> None:
        cur = self.conn.cursor()
        cur.executemany(
            """INSERT INTO drivers (driver_id, carrier_id, driver_name, phone, licence_number, home_base_city,
                   driver_status) VALUES (%s, %s, %s, %s, %s, %s, 'ACTIVE')""",
            [(t.driver_id, t.carrier_id, t.driver_name, f"+91-6300{t.driver_id[-4:]}{t.driver_id[-2:]}",
              f"RJ-GEN-{t.driver_id[-4:]}", t.origin) for t in trucks])
        cur.executemany(
            """INSERT INTO vehicles (vehicle_id, carrier_id, vehicle_type_code, registration_number, capacity_kg,
                   refrigeration_capable, active_flag) VALUES (%s, %s, %s, %s, %s, %s, 1)""",
            [(t.vehicle_id, t.carrier_id, t.vehicle_type, f"RJ14 GX {t.vehicle_id[-4:]}", t.capacity_kg,
              int(t.refrigerated)) for t in trucks])
        cur.executemany(
            """INSERT INTO shipments (shipment_id, order_reference, carrier_id, driver_id, vehicle_id, origin_name,
                   origin_city, destination_facility_id, customer_name, product_category, load_weight_kg,
                   pallet_count, required_dock_type, temperature_control_required, priority_code,
                   planned_departure_ts, actual_departure_ts, original_eta_ts, latest_eta_ts, expected_unload_min,
                   current_status, created_at, updated_at)
               VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)""",
            [(t.shipment_id, f"ORD-{t.shipment_id[4:]}", t.carrier_id, t.driver_id, t.vehicle_id,
              f"{t.origin} depot", t.origin, self.facility, t.customer, t.product, t.load_kg, t.pallets,
              t.dock_type, int(t.temperature), t.priority, t.departure,
              t.departure if t.departure <= t.created_at else None, t.eta,
              t.eta_updates[-1][1] if t.eta_updates else t.eta, t.unload_min,
              "IN_TRANSIT" if t.departure <= t.created_at else "ASSIGNED", t.created_at, t.created_at)
             for t in trucks])
        updates = [(f"ETA-{t.shipment_id[4:]}-{i + 1}", t.shipment_id, t.driver_id, new_eta, conf, reason,
                    "generated disruption", reported)
                   for t in trucks for i, (reported, new_eta, conf, reason) in enumerate(t.eta_updates)]
        cur.executemany(
            """INSERT INTO eta_updates (eta_update_id, shipment_id, source_type, reported_by_driver_id,
                   declared_eta_ts, confidence_code, delay_reason_code, note, created_at)
               VALUES (%s, %s, 'DRIVER_DECLARED', %s, %s, %s, %s, %s, %s)""", updates)
        for t in trucks:
            if t.booking_dock:
                self._book_on(t)

    def _book_on(self, t: Truck) -> bool:
        """Book a coordinator case on the first free OPEN slot of a (too light) dock after it can arrive."""
        ready = t.eta + dt.timedelta(minutes=30)
        row = self.conn.execute(
            """SELECT sl.slot_id FROM appointment_slots sl JOIN docks d ON d.dock_id = sl.dock_id
               WHERE d.facility_id = %s AND d.dock_code = %s AND sl.slot_status = 'OPEN' AND sl.slot_start_ts >= %s
                 AND NOT EXISTS (SELECT 1 FROM appointments a WHERE a.slot_id = sl.slot_id
                                 AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS'))
               ORDER BY sl.slot_start_ts LIMIT 1""", (self.facility, t.booking_dock, ready)).fetchone()
        if row is None:
            return False
        self.conn.execute(
            """INSERT INTO appointments (appointment_id, shipment_id, slot_id, appointment_status, booking_source,
                   is_current, booked_at, confirmed_at, updated_at)
               VALUES (%s, %s, %s, 'CONFIRMED', 'PLANNER', 1, %s, %s, %s)""",
            (f"APT-{t.shipment_id[4:]}", t.shipment_id, row[0], t.created_at, t.created_at, t.created_at))
        return True


def generate(conn: psycopg.Connection, *, trucks: int = 70, trickle: int = 20, seed: int = 42,
             disruptions: bool = True, coordinator_cases: int = 4, facility: str = FACILITY,
             now: dt.datetime | None = None) -> Summary:
    """Generate a scenario as of `now` (default the shared clock). One transaction."""
    if trucks < 0 or trickle < 0 or trickle > trucks:
        raise ValueError("need 0 <= trickle <= trucks")
    if coordinator_cases > trucks - trickle:
        raise ValueError("coordinator cases come out of the trucks known at the start")
    with conn.transaction():
        if now is None:
            (now,) = conn.execute("SELECT setuhaul_now()").fetchone()
        now = now.astimezone(ZONE)
        day = now.date()
        last_regular = _local(day, LAST_ARRIVAL)
        g = Generator(conn, facility, seed)
        out: list[Truck] = []

        at_start = trucks - trickle
        regular = at_start - coordinator_cases
        for _ in range(regular):
            created = now - dt.timedelta(minutes=g.rng.randint(10, 240))
            out.append(g.truck(created.replace(microsecond=0),
                               g.eta_between(now + dt.timedelta(minutes=25), last_regular)))
        breaches = coordinator_cases // 2
        for i in range(breaches):
            # A 21-23.5 t load booked on a 20 t dock: flagged for a coordinator, never moved by the solver.
            t = g.truck((now - dt.timedelta(minutes=90)).replace(microsecond=0),
                        g.eta_between(now + dt.timedelta(minutes=60), now + dt.timedelta(hours=6)), kind="STANDARD")
            t.vehicle_type, t.capacity_kg, t.load_kg = "32FT_MXL", 22000, g.rng.randint(20500, 21900)
            t.booking_dock = ["D1", "D3"][i % 2]
            out.append(t)
        for _ in range(coordinator_cases - breaches):
            # Arrives too late to start before the facility's last start time.
            t = g.truck((now - dt.timedelta(minutes=60)).replace(microsecond=0),
                        g.eta_between(_local(day, dt.time(20, 45)), _local(day, dt.time(21, 30))))
            out.append(t)

        window_from = max(now, _local(day, dt.time(10, 0)))
        window_to = _local(day, dt.time(17, 0))
        for _ in range(trickle):
            created = g.eta_between(window_from, window_to) if window_to > window_from else now
            out.append(g.truck(created, g.eta_between(created + dt.timedelta(minutes=60),
                                                      min(created + dt.timedelta(hours=6), last_regular))))

        if disruptions:
            for t in out:
                if not t.booking_dock and g.rng.random() < 0.25:
                    g.disrupt(t, now)
        g.insert(out)

        events = 0
        if disruptions:
            event_at = _local(day, dt.time(14, 0))
            if event_at > now:
                conn.execute(
                    """INSERT INTO dock_status_events (dock_event_id, dock_id, event_type, event_start_ts,
                           event_end_ts, reason, created_at)
                       SELECT %s, dock_id, 'BREAKDOWN', %s, %s, 'Generated: hydraulic leveller fault', %s
                       FROM docks WHERE facility_id = %s AND dock_code = 'D4'
                       ON CONFLICT (dock_event_id) DO NOTHING""",
                    (f"EVT-{PREFIX}-{day:%m%d}-D4", event_at, event_at + dt.timedelta(minutes=90),
                     event_at - dt.timedelta(minutes=20), facility))
                events = 1

        return Summary(
            created=[t.shipment_id for t in out], at_start=at_start, trickle=trickle,
            eta_updates=sum(len(t.eta_updates) for t in out), dock_events=events,
            weight_breaches=breaches, late_arrivals=coordinator_cases - breaches)


def add_now(conn: psycopg.Connection, n: int, *, seed: int, facility: str = FACILITY,
            disruptions: bool = True) -> list[str]:
    """n truck requests made now (the "Add trucks" button), arriving one to six hours from now."""
    with conn.transaction():
        (now,) = conn.execute("SELECT setuhaul_now()").fetchone()
        now = now.astimezone(ZONE)
        g = Generator(conn, facility, seed)
        last = max(now + dt.timedelta(hours=1), _local(now.date(), LAST_ARRIVAL))
        trucks = [g.truck(now.replace(microsecond=0),
                          g.eta_between(now + dt.timedelta(minutes=60), min(now + dt.timedelta(hours=6), last)))
                  for _ in range(n)]
        if disruptions:
            for t in trucks:
                if g.rng.random() < 0.25:
                    g.disrupt(t, now)
        g.insert(trucks)
        return [t.shipment_id for t in trucks]


def process_jobs(conn: psycopg.Connection) -> list[tuple[int, str]]:
    """Fulfil queued "Add trucks" requests. Returns (job id, result) for each job handled."""
    done = []
    while True:
        with conn.transaction():
            row = conn.execute(
                """SELECT job_id, facility_id, trucks FROM truck_request_jobs WHERE status = 'PENDING'
                   ORDER BY job_id LIMIT 1 FOR UPDATE SKIP LOCKED""").fetchone()
            if row is None:
                return done
            job_id, facility, n = row
            try:
                with conn.transaction():
                    ids = add_now(conn, n, seed=job_id * 7919, facility=facility)
                result, status = f"{len(ids)} trucks: {ids[0]} to {ids[-1]}", "DONE"
            except (ValueError, psycopg.Error) as exc:
                result, status = f"failed: {exc}", "FAILED"
            conn.execute("UPDATE truck_request_jobs SET status = %s, processed_at = now(), result = %s "
                         "WHERE job_id = %s", (status, result, job_id))
            done.append((job_id, result))


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--database-url", default=os.environ.get("DATABASE_URL"))
    parser.add_argument("--facility", default=FACILITY)
    parser.add_argument("--trucks", type=int, default=int(os.environ.get("SETUHAUL_SCENARIO_TRUCKS", "70")))
    parser.add_argument("--trickle", type=int, default=int(os.environ.get("SETUHAUL_SCENARIO_TRICKLE", "20")))
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument("--coordinator-cases", type=int, default=4)
    parser.add_argument("--no-disruptions", action="store_true")
    parser.add_argument("--once", action="store_true", help="do nothing if generated trucks already exist")
    args = parser.parse_args(argv)
    if not args.database_url:
        parser.error("pass --database-url or set DATABASE_URL")
    if args.trucks == 0:
        print("no extra trucks requested")
        return 0
    try:
        with psycopg.connect(args.database_url) as conn:
            if args.once:
                (n,) = conn.execute("SELECT count(*) FROM shipments WHERE shipment_id LIKE %s",
                                    (f"SHP-{PREFIX}%",)).fetchone()
                if n:
                    print(f"scenario already loaded ({n} generated trucks); nothing added")
                    return 0
            summary = generate(conn, trucks=args.trucks, trickle=min(args.trickle, args.trucks), seed=args.seed,
                               disruptions=not args.no_disruptions,
                               coordinator_cases=min(args.coordinator_cases, args.trucks - min(args.trickle, args.trucks)),
                               facility=args.facility)
    except (ValueError, psycopg.Error) as exc:
        print(f"scenario failed: {exc}", file=sys.stderr)
        return 1
    print(summary)
    return 0


if __name__ == "__main__":
    sys.exit(main())
