import assert from 'node:assert/strict';
import * as THREE from 'three';
import {addressBlock,buildTopology,geoDirection,globePoint,linkPath,neighborhood,roleFor,rotate} from '../components/map/topology.ts';

const device=(i,extra={})=>({id:`d${i}`,name:`host-${i}`,status:'online',classification:'unknown',primaryIpAddress:`192.168.1.${i}`,firstSeenAt:'',lastSeenAt:'',...extra});
const flow=(id,localIp,remoteIp,extra={})=>({id,localIp,remoteIp,localPort:1,remotePort:443,localPorts:[1],remotePorts:[443],flowCount:1,direction:'outbound',protocol:'TCP',application:'TLS',bytes:1,bytesPerSecond:1000,firstSeenAt:'',lastSeenAt:'',...extra});

const empty=buildTopology({devices:[],flows:[]});
assert.equal(empty.nodes.length,0);assert.equal(empty.links.length,0);assert.equal(empty.groups.length,0);
assert.ok(Number.isFinite(empty.extent)&&empty.extent>0,'empty topology still has a camera extent');

assert.equal(roleFor('Router'),'gateway');assert.equal(roleFor('access point'),'switch');assert.equal(roleFor('NAS'),'server');
assert.equal(roleFor('laptop'),'client');assert.equal(roleFor('unknown'),'unknown');assert.equal(roleFor(undefined),'unknown');
assert.equal(addressBlock('10.0.3.7').label,'10.0.3.0/24');assert.equal(addressBlock('fe80::1').id,'v6');assert.equal(addressBlock(undefined).id,'none');

const devices=[device(1,{classification:'router'}),device(2,{classification:'switch'}),device(3,{status:'offline'}),device(4,{classification:'server'}),device(5,{primaryIpAddress:'10.0.0.5'})];
const flows=[
 flow('f1','192.168.1.4','8.8.8.8',{remoteLatitude:37.4,remoteLongitude:-122.1,remoteCountryCode:'US',remoteCountryName:'United States',bytesPerSecond:5000}),
 flow('f2','192.168.1.4','8.8.8.8',{application:'QUIC',remoteLatitude:37.4,remoteLongitude:-122.1,remoteCountryCode:'US',remoteCountryName:'United States'}),
 flow('f3','192.168.1.4','8.8.8.8',{direction:'inbound',remoteLatitude:37.4,remoteLongitude:-122.1}),
 flow('f4','192.168.1.99','1.1.1.1'),
];
const topology=buildTopology({devices,flows});
const byId=new Map(topology.nodes.map(n=>[n.id,n]));
assert.deepEqual(topology.groups.map(g=>g.label),['10.0.0.0/24','192.168.1.0/24'],'plates sorted by address');
assert.equal(byId.get('host:192.168.1.99')?.kind,'observed','traffic-only local IP stays visible as observed');
assert.equal(topology.links.length,3,'links aggregate per local, remote and direction');
const out=topology.links.find(l=>l.source==='device:d4'&&l.direction==='outbound');
assert.deepEqual(out.flowIds,['f1','f2']);assert.deepEqual(out.applications,['TLS','QUIC']);assert.equal(out.bytesPerSecond,6000);
assert.equal(byId.get('device:d1').bytesPerSecond,0,'no traffic is invented for devices without flows');
assert.equal(byId.get('remote:1.1.1.1').located,false);
const [ux,uy,uz]=byId.get('remote:1.1.1.1').position,[gx,gy,gz]=topology.globe.unlocated;
assert.ok(Math.hypot(ux-gx,uy-gy,uz-gz)<3,'unlocated endpoints gather in their own cluster');
const plate=topology.nodes.filter(n=>n.groupId==='v4:192.168.1');
assert.ok(plate.findIndex(n=>n.role==='gateway')<plate.findIndex(n=>n.role==='unknown'),'infrastructure is laid out before endpoints');
assert.ok(byId.get('device:d1').position[2]<byId.get('device:d3').position[2],'infrastructure row sits behind endpoints');
assert.equal(topology.countries[0]?.code,'US');

const shuffled=buildTopology({devices:[...devices].reverse(),flows:[...flows].reverse()});
for(const node of topology.nodes) assert.deepEqual(new Map(shuffled.nodes.map(n=>[n.id,n.position])).get(node.id),node.position,`stable position for ${node.id}`);

// Pins must land where the globe texture shows that place: compare with three.js SphereGeometry UVs
// (equirectangular: u = (lon + 180) / 360, v = (lat + 90) / 180) and with the mesh's Euler rotation.
const sphere=new THREE.SphereGeometry(1,72,36),position=sphere.getAttribute('position'),uv=sphere.getAttribute('uv');
for(let i=0;i<position.count;i+=7){
 const lon=uv.getX(i)*360-180,lat=uv.getY(i)*180-90,d=geoDirection(lat,lon);
 assert.ok(Math.hypot(d[0]-position.getX(i),d[1]-position.getY(i),d[2]-position.getZ(i))<1e-6,`texture mapping at ${lat},${lon}`);
}
const berlin={lat:52.52,lon:13.405},sited=buildTopology({devices,flows,site:berlin}),g=sited.globe;
for(const [lat,lon] of [[0,0],[52.5,13.4],[-33.9,151.2],[37.4,-122.1],[64,-150]]){
 const expected=new THREE.Vector3(...geoDirection(lat,lon)).applyEuler(new THREE.Euler(g.gamma,g.alpha,0,'XYZ')),actual=rotate(geoDirection(lat,lon),g.alpha,g.gamma);
 assert.ok(expected.distanceTo(new THREE.Vector3(...actual))<1e-9,`globe rotation matches mesh at ${lat},${lon}`);
}
const unit=p=>{const v=p.map((x,i)=>x-g.center[i]),l=Math.hypot(...v);return v.map(x=>x/l);};
const site=unit(globePoint(g,berlin.lat,berlin.lon));
assert.ok(Math.abs(site[0])<1e-9&&site[1]<0&&site[2]>.9,'local site faces the network and default camera');
assert.ok(unit(globePoint(g,berlin.lat,berlin.lon+30))[0]>site[0]+.1,'east appears to the right, not mirrored');
assert.ok(unit(globePoint(g,berlin.lat+20,berlin.lon))[1]>site[1]+.1,'north appears above');
assert.deepEqual(g.uplink,globePoint(g,berlin.lat,berlin.lon),'with a local site the uplink is the site itself');
const unsited=buildTopology({devices,flows}).globe;
assert.ok(Math.hypot(...unit(unsited.uplink))>0&&Math.hypot(...unsited.uplink.map((v,i)=>v-unsited.center[i]))>unsited.radius*1.2,'without a site the uplink floats off the globe');
assert.ok(g.center[1]-g.radius>3,'globe floats above the local network');

const pin=sited.nodes.find(n=>n.id==='remote:8.8.8.8'),pinDir=unit(pin.position),exact=unit(globePoint(g,37.4,-122.1));
assert.ok(Math.hypot(...pinDir.map((v,i)=>v-exact[i]))<1e-9,'pins sit exactly on their coordinates');
const twins=buildTopology({devices,flows:[flow('a','192.168.1.4','9.9.9.1',{remoteLatitude:48.1,remoteLongitude:11.6}),flow('b','192.168.1.4','9.9.9.2',{remoteLatitude:48.1,remoteLongitude:11.6})],site:berlin});
const [t1,t2]=twins.nodes.filter(n=>n.kind==='remote').map(n=>n.position),r=p=>Math.hypot(...p.map((v,i)=>v-twins.globe.center[i]));
assert.ok(Math.abs(r(t1)-r(t2))>.1,'endpoints sharing a location stack outward');

const start=[1,1,1],route=linkPath(g,start,pin,'outbound');
assert.ok(Math.hypot(...route[0].map((v,i)=>v-start[i]))<1e-6&&Math.hypot(...route.at(-1).map((v,i)=>v-pin.position[i]))<1e-6,'outbound route runs from device to pin');
const uplinkIndex=route.findIndex(p=>Math.hypot(...p.map((v,i)=>v-g.uplink[i]))<g.radius*.2);
assert.ok(uplinkIndex>0&&route.slice(uplinkIndex+1).every(p=>Math.hypot(...p.map((v,i)=>v-g.center[i]))>=g.radius*.99),'route passes the uplink and stays above the globe surface');
assert.deepEqual(linkPath(g,start,pin,'inbound'),[...route].reverse(),'inbound routes run from pin to device');
const antipode=buildTopology({devices,flows:[flow('x','192.168.1.4','7.7.7.7',{remoteLatitude:-berlin.lat,remoteLongitude:berlin.lon-180})],site:berlin});
assert.ok(linkPath(antipode.globe,start,antipode.nodes.find(n=>n.kind==='remote'),'outbound').every(p=>p.every(Number.isFinite)),'antipodal routes stay finite');

assert.equal(buildTopology({devices,flows,deviceFilter:'online'}).nodes.some(n=>n.id==='device:d3'),false);
assert.deepEqual(buildTopology({devices,flows,deviceFilter:'traffic'}).nodes.filter(n=>n.kind==='device').map(n=>n.id),['device:d4']);

const focus=neighborhood(topology,'device:d4',null);
assert.ok(focus.active&&focus.nodes.has('remote:8.8.8.8')&&focus.links.size===2&&!focus.nodes.has('device:d1'));
assert.equal(neighborhood(topology,null,'f4').links.size,1);

const large=buildTopology({devices:Array.from({length:600},(_,i)=>device(i,{primaryIpAddress:`10.${Math.floor(i/200)}.${Math.floor(i/100)%2}.${i%100+1}`,classification:['router','switch','server','laptop','unknown'][i%5]})),flows:[]});
const locals=large.nodes;
for(let i=0;i<locals.length;i++) for(let j=i+1;j<locals.length;j++) {
 const a=locals[i].position,b=locals[j].position;
 assert.ok(Math.hypot(a[0]-b[0],a[2]-b[2])>2,'devices never overlap');
}
assert.ok(large.globe.center[1]-large.globe.radius>3,'globe stays above large networks');
console.log(`Topology layout, grouping, aggregation, determinism and globe placement passed (${locals.length} hosts, ${large.groups.length} plates).`);
