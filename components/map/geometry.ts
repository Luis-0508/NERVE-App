import { geoNaturalEarth1, geoPath, geoGraticule10, geoInterpolate } from "d3-geo";

export type Point = { lat: number; lon: number };
export const WIDTH = 1200, HEIGHT = 640;
export const projection = geoNaturalEarth1().fitExtent([[24, 48], [WIDTH - 24, HEIGHT - 32]], { type: "Sphere" });
export const geographyPath = geoPath(projection);
export const graticulePath = geographyPath(geoGraticule10()) ?? "";
export function validPoint(p: Point) { return Number.isFinite(p.lat) && Number.isFinite(p.lon) && Math.abs(p.lat) <= 90 && Math.abs(p.lon) <= 180; }
export function project(p: Point) { return projection([p.lon, p.lat])!; }
// Great-circle interpolation follows the short route, including across the date line.
// A small screen-space lift creates the raised arc; discontinuities are never joined.
export function arcPoints(from: Point, to: Point): [number, number][] {
 const interpolate = geoInterpolate([from.lon, from.lat], [to.lon, to.lat]);
 const distance = Math.hypot(...project(from).map((v, i) => v - project(to)[i]));
 return Array.from({ length: 65 }, (_, i) => {
  const t = i / 64, p = projection(interpolate(t))!;
  return [p[0], p[1] - Math.sin(Math.PI * t) * Math.min(42, distance * .12)];
 });
}
