// Versioned provenance. Unversioned coordinates are candidates, never active sites.
export const siteKey = "nerve:traffic-map-local-site:v2";
export const legacySiteKey = "nerve:traffic-map-origin";
export type SitePoint = { lat: number; lon: number };
export type LocalSite = SitePoint & { version: 2; source: "manual" | "browser"; savedAt: string; accuracyMeters?: number };
type StorageReader = Pick<Storage, "getItem">;
function point(value: unknown): value is SitePoint & Record<string, unknown> {
 if (!value || typeof value !== "object") return false;
 const p = value as SitePoint;
 return Number.isFinite(p.lat) && Number.isFinite(p.lon) && Math.abs(p.lat) <= 90 && Math.abs(p.lon) <= 180;
}
export function readLocalSite(storage: StorageReader, now = Date.now()): { site: LocalSite | null; candidate: SitePoint | null; notice: string } {
 try {
  const raw = storage.getItem(siteKey);
  if (raw !== null) {
   const value = JSON.parse(raw);
   if (value?.version === 2 && value.source === "unset") return {site:null,candidate:null,notice:""};
   const time = Date.parse(value?.savedAt);
   if (point(value) && value.version === 2 && (value.source === "manual" || value.source === "browser") && Number.isFinite(time) && time <= now + 60_000) {
    if (value.source === "manual" || (typeof value.accuracyMeters === "number" && Number.isFinite(value.accuracyMeters) && value.accuracyMeters >= 0 && now-time <= 30*86400_000)) return {site:value as LocalSite,candidate:null,notice:""};
   }
   return {site:null,candidate:point(value)?value:null,notice:"Saved location needs confirmation. Its provenance or freshness could not be verified."};
  }
  const legacy = storage.getItem(legacySiteKey);
  if (legacy !== null) {
   let value: unknown; try {value=JSON.parse(legacy);} catch {value=null;}
   return {site:null,candidate:point(value)?value:null,notice:"An older saved location has no source or timestamp. It is not being used. Review it and save explicitly, or choose a new location."};
  }
 } catch { return {site:null,candidate:null,notice:"Saved location could not be read. Set a location for this visit."}; }
 return {site:null,candidate:null,notice:""};
}
