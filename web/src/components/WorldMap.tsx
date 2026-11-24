import { geoNaturalEarth1, geoPath } from "d3-geo"
import type { Feature, FeatureCollection, Geometry } from "geojson"
import countryCodes from "i18n-iso-countries"
import { useMemo } from "react"
import { feature } from "topojson-client"
import type { GeometryCollection, Topology } from "topojson-specification"
import world from "world-atlas/countries-110m.json"

import { countryName } from "@/lib/waf"

/** One country's requests, for the map to shade. */
export interface MapCountry {
  code: string
  requests: number
  blocked: number
}

const WIDTH = 960
const HEIGHT = 480

interface Frame {
  outline: string
  shapes: { id: string; name: string; d: string }[]
}

let frameCache: Frame | null = null

/**
 * The land shapes and the sphere's outline, projected once. The atlas is Natural
 * Earth at 110 m, public domain. The projection fits the map's own frame.
 */
function frame(): Frame {
  if (frameCache) return frameCache
  const topo = world as unknown as Topology
  const countries = feature(topo, topo.objects.countries as GeometryCollection) as unknown as FeatureCollection<
    Geometry,
    { name?: string }
  >
  const projection = geoNaturalEarth1().fitExtent(
    [
      [8, 8],
      [WIDTH - 8, HEIGHT - 8],
    ],
    { type: "Sphere" },
  )
  const path = geoPath(projection)
  frameCache = {
    outline: path({ type: "Sphere" }) ?? "",
    shapes: countries.features.map((f: Feature<Geometry, { name?: string }>) => ({
      id: String(f.id ?? ""),
      name: f.properties?.name ?? "",
      d: path(f) ?? "",
    })),
  }
  return frameCache
}

/**
 * A choropleth of request sources. Each country is shaded by its share of the
 * busiest country's requests; countries with no requests stay grey. Each shape
 * has a tooltip with its figures, and the table beside the map lists the same
 * numbers, so the map is never the only way to read them.
 */
export function WorldMap({
  countries,
  label,
  metric = "requests",
}: {
  countries: MapCountry[]
  label: string
  /** Which count shades the countries: every request, or only the blocked ones. */
  metric?: "requests" | "blocked"
}) {
  const { outline, shapes } = useMemo(() => frame(), [])

  const byNumeric = useMemo(() => {
    const m = new Map<string, MapCountry>()
    for (const c of countries) {
      const numeric = countryCodes.alpha2ToNumeric(c.code)
      if (numeric) m.set(numeric, c)
    }
    return m
  }, [countries])

  const value = (c: MapCountry) => (metric === "requests" ? c.requests : c.blocked)
  const busiest = Math.max(1, ...countries.map(value))

  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
      className="h-52 w-full md:h-64"
      preserveAspectRatio="xMidYMid meet"
    >
      <path d={outline} fill="none" stroke="var(--border)" />
      {shapes.map((s) => {
        const c = byNumeric.get(s.id)
        const seen = c !== undefined && value(c) > 0
        const tip = c
          ? `${countryName(c.code)}: ${c.requests.toLocaleString()} requests, ${c.blocked.toLocaleString()} blocked`
          : s.name
        return (
          <path
            key={s.id || s.name}
            d={s.d}
            stroke="var(--border)"
            strokeWidth={0.4}
            fill={seen ? "var(--primary)" : "var(--muted)"}
            fillOpacity={c && seen ? 0.2 + 0.8 * (value(c) / busiest) : 1}
          >
            <title>{tip}</title>
          </path>
        )
      })}
    </svg>
  )
}
