"""Distances and positions along a route. Coordinates are (lat, lon) in degrees; distances in km."""

from __future__ import annotations

import bisect
import math
from dataclasses import dataclass

EARTH_RADIUS_KM = 6371.0088


def haversine_km(a: tuple[float, float], b: tuple[float, float]) -> float:
    lat1, lon1, lat2, lon2 = map(math.radians, (a[0], a[1], b[0], b[1]))
    h = math.sin((lat2 - lat1) / 2) ** 2 + math.cos(lat1) * math.cos(lat2) * math.sin((lon2 - lon1) / 2) ** 2
    return 2 * EARTH_RADIUS_KM * math.asin(math.sqrt(h))


def bearing_deg(a: tuple[float, float], b: tuple[float, float]) -> float:
    lat1, lon1, lat2, lon2 = map(math.radians, (a[0], a[1], b[0], b[1]))
    y = math.sin(lon2 - lon1) * math.cos(lat2)
    x = math.cos(lat1) * math.sin(lat2) - math.sin(lat1) * math.cos(lat2) * math.cos(lon2 - lon1)
    return (math.degrees(math.atan2(y, x)) + 360) % 360


@dataclass(frozen=True)
class Route:
    """A polyline with the cumulative distance at each vertex."""

    points: tuple[tuple[float, float], ...]
    cumulative_km: tuple[float, ...]

    @classmethod
    def from_lonlat(cls, coordinates: list[list[float]]) -> Route:
        points = tuple((lat, lon) for lon, lat, *_ in coordinates)
        if len(points) < 2:
            raise ValueError("a route needs at least two points")
        cum = [0.0]
        for p, q in zip(points, points[1:]):
            cum.append(cum[-1] + haversine_km(p, q))
        return cls(points, tuple(cum))

    @property
    def length_km(self) -> float:
        return self.cumulative_km[-1]

    def position_at(self, km: float) -> tuple[tuple[float, float], float]:
        """The point `km` along the route (clamped to its ends) and the heading there."""
        km = min(max(km, 0.0), self.length_km)
        i = max(1, bisect.bisect_left(self.cumulative_km, km))
        i = min(i, len(self.points) - 1)
        a, b = self.points[i - 1], self.points[i]
        seg = self.cumulative_km[i] - self.cumulative_km[i - 1]
        f = 0.0 if seg == 0 else (km - self.cumulative_km[i - 1]) / seg
        point = (a[0] + (b[0] - a[0]) * f, a[1] + (b[1] - a[1]) * f)
        return point, bearing_deg(a, b)

    def nearest_km(self, point: tuple[float, float], hint_index: int | None = None, window: int = 400) -> tuple[float, float, int]:
        """(km along the route, distance from the route, vertex index) of the vertex nearest to `point`.

        With `hint_index`, only a window around it is searched first (pings move forward), then the whole route
        if the best match there is poor.
        """
        def search(lo: int, hi: int) -> tuple[float, int]:
            best, best_i = math.inf, lo
            for i in range(lo, hi):
                d = haversine_km(point, self.points[i])
                if d < best:
                    best, best_i = d, i
            return best, best_i

        n = len(self.points)
        if hint_index is not None:
            d, i = search(max(0, hint_index - window), min(n, hint_index + window))
            if d < 2.0:
                return self.cumulative_km[i], d, i
        d, i = search(0, n)
        return self.cumulative_km[i], d, i
