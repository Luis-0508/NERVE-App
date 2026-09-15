import assert from 'node:assert/strict';
import fs from 'node:fs';
import { feature } from 'topojson-client';
import { geoContains } from 'd3-geo';
import {readLocalSite,siteKey,legacySiteKey} from '../components/map/local-site.ts';
import {arcPoints,project,projection,validPoint,WIDTH} from '../components/map/geometry.ts';
for(const point of [{lat:0,lon:0},{lat:52.52,lon:13.405},{lat:-33.9,lon:151.2},{lat:90,lon:180}]){
 const projected=project(point), inverted=projection.invert(projected);
 assert.ok(Math.abs(((inverted[0]-point.lon+540)%360)-180)<1e-5 && Math.abs(inverted[1]-point.lat)<1e-5,'projection round trip');
}
const origin={lat:35,lon:175},target={lat:40,lon:-175};
const points=arcPoints(origin,target);
assert.deepEqual(points[0],project(origin));
assert.ok(Math.hypot(...points.at(-1).map((v,i)=>v-project(target)[i]))<1e-9,'arc target');
assert.ok(points.some((p,i)=>i&&Math.abs(p[0]-points[i-1][0])>WIDTH/2),'date-line route must split');
assert.ok(!validPoint({lat:91,lon:0})&&!validPoint({lat:0,lon:181})&&!validPoint({lat:NaN,lon:0}));
console.log('Map projection, endpoint alignment, date-line split and location validation passed.');
const world=JSON.parse(fs.readFileSync('public/maps/countries-50m.json','utf8'));
const countries=feature(world,world.objects.countries).features;
const locations=JSON.parse(fs.readFileSync('internal/traffic/country-locations.json','utf8'));
for(const [code,p] of Object.entries(locations)) {
 const country=countries.find(f=>String(f.id)===p.ID);
 assert.ok(country && geoContains(country,[p.Lon,p.Lat]),`${code} representative outside own country`);
}
assert.ok(!geoContains(countries.find(f=>String(f.id)==='276'),[-39.849,13.990]),'Atlantic is not Germany');
const store=new Map();const storage={getItem:key=>store.get(key)??null};
assert.equal(readLocalSite(storage).site,null);
store.set(legacySiteKey,JSON.stringify({lat:13.990,lon:-39.849}));
assert.equal(readLocalSite(storage).site,null);
assert.deepEqual(readLocalSite(storage).candidate,{lat:13.990,lon:-39.849});
assert.ok(store.has(legacySiteKey),'legacy value retained for review');
store.set(legacySiteKey,JSON.stringify({lat:999,lon:0}));
assert.equal(readLocalSite(storage).candidate,null);
const manual={version:2,source:'manual',lat:53.55,lon:9.99,savedAt:'2020-01-01T00:00:00Z'};
store.set(siteKey,JSON.stringify(manual));
assert.deepEqual(readLocalSite(storage).site,manual,'explicit manual value does not expire');
store.set(siteKey,JSON.stringify({...manual,source:'browser',accuracyMeters:500}));
assert.equal(readLocalSite(storage).site,null,'stale browser estimate requires confirmation');
store.set(siteKey,JSON.stringify({...manual,source:'browser',savedAt:new Date().toISOString(),accuracyMeters:500}));
assert.equal(readLocalSite(storage).site.accuracyMeters,500);
store.set(siteKey,JSON.stringify({...manual,source:'unknown'}));assert.equal(readLocalSite(storage).site,null);
store.set(siteKey,JSON.stringify({version:2,source:'unset'}));assert.equal(readLocalSite(storage).candidate,null);
console.log('All country representatives on own land; local-site provenance, stale/invalid migration and persistence passed.');
