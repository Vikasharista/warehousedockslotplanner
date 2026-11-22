// Times are shown in the facility's zone (Asia/Kolkata), whatever the viewer's own zone is.
export const ZONE = "Asia/Kolkata";

const hm = new Intl.DateTimeFormat("en-GB", { timeZone: ZONE, hour: "2-digit", minute: "2-digit", hour12: false });
const hms = new Intl.DateTimeFormat("en-GB", {
  timeZone: ZONE,
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});
const day = new Intl.DateTimeFormat("en-GB", { timeZone: ZONE, weekday: "short", day: "numeric", month: "short" });

export const clock = (t: string | number | Date) => hm.format(new Date(t));
export const clockSeconds = (t: string | number | Date) => hms.format(new Date(t));
export const date = (t: string | number | Date) => day.format(new Date(t));

/** Position of `t` across [start, end] as a percentage, clamped to 0..100. */
export function pct(t: number, start: number, end: number): number {
  if (end <= start) return 0;
  return Math.min(100, Math.max(0, ((t - start) / (end - start)) * 100));
}

/** Whole-hour tick times inside [start, end]. */
export function hourTicks(start: number, end: number): number[] {
  const hour = 3_600_000;
  const out: number[] = [];
  for (let t = Math.ceil(start / hour) * hour; t <= end; t += hour) out.push(t);
  return out;
}

/** "in 12 min", "3 min ago". */
export function relative(t: number, now: number): string {
  const min = Math.round((t - now) / 60_000);
  if (min === 0) return "now";
  return min > 0 ? `in ${min} min` : `${-min} min ago`;
}

/** Minutes between two instants, rounded. */
export const minutesBetween = (a: string, b: string) => Math.round((new Date(b).getTime() - new Date(a).getTime()) / 60_000);
