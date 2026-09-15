// Input: Natural Earth v5.1.2 ne_50m_admin_0_countries.geojson (public domain).
// Validate label points against the exact locally rendered world-atlas geometry.
import fs from 'node:fs';
import { createHash } from 'node:crypto';
import { feature } from 'topojson-client';
import { geoContains, geoCentroid, geoArea, geoBounds } from 'd3-geo';
const bytes = fs.readFileSync(process.argv[2]);
if(createHash('sha256').update(bytes).digest('hex') !== '3e458fc036ad0a66411f2c1e6cac49c5d7bfb81cb1123bc513b22511a2b7fdeb') throw Error('Expected pinned Natural Earth v5.1.2 input');
const input = JSON.parse(bytes);
const world = JSON.parse(fs.readFileSync('public/maps/countries-50m.json', 'utf8'));
const countries = feature(world, world.objects.countries).features;
const result = {};
for (const source of input.features) {
 const p = source.properties, code = p.ISO_A2_EH;
 if (!/^[A-Z]{2}$/.test(code)) continue;
 const country = countries.find(f => String(f.id) === String(p.ISO_N3_EH));
 // Missing/ambiguous geometry must remain unlocated, never borrow another country.
 if (!country || String(p.ISO_N3_EH) === '-99') continue;
 let point = [p.LABEL_X, p.LABEL_Y];
 if (!point.every(Number.isFinite) || !geoContains(country, point)) {
  const polygons = country.geometry.type === 'MultiPolygon' ? country.geometry.coordinates : [country.geometry.coordinates];
  const largest = polygons.map(coordinates => ({type:'Polygon',coordinates})).sort((a,b) => geoArea(b)-geoArea(a))[0];
  point = geoCentroid(largest);
  if (!geoContains(largest, point)) {
   const [[west,south],[east,north]] = geoBounds(largest);
   const span = (east-west+360)%360 || 360;
   point = null;
   // Deterministic interior candidate, only accepted after spherical containment.
   for(let y=1;y<128 && !point;y++) for(let x=1;x<128 && !point;x++) {
    const candidate = [((west+span*x/128+540)%360)-180,south+(north-south)*y/128];
    if(geoContains(largest,candidate)) point=candidate;
   }
  }
 }
 if (!point || !geoContains(country, point)) throw Error(`No land representative for ${code}`);
 result[code] = {Name:p.NAME_EN, ID:String(country.id), Lat:point[1], Lon:point[0]};
}
fs.writeFileSync('internal/traffic/country-locations.json', JSON.stringify(result,null,2)+'\n');
console.log(`Generated ${Object.keys(result).length} country representatives, all inside rendered country geometry.`);
