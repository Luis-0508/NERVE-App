import assert from 'node:assert/strict';
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
