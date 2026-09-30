import assert from 'node:assert/strict';
import {addressBlock,buildTopology,cylinderPoint,neighborhood,roleFor} from '../components/map/topology.ts';

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
assert.ok(byId.get('remote:1.1.1.1').position[1]>topology.horizon.height+topology.horizon.band,'unlocated endpoints sit above the horizon band');
const plate=topology.nodes.filter(n=>n.groupId==='v4:192.168.1');
assert.ok(plate.findIndex(n=>n.role==='gateway')<plate.findIndex(n=>n.role==='unknown'),'infrastructure is laid out before endpoints');
assert.ok(byId.get('device:d1').position[2]<byId.get('device:d3').position[2],'infrastructure row sits behind endpoints');
assert.equal(topology.countries[0]?.code,'US');

const shuffled=buildTopology({devices:[...devices].reverse(),flows:[...flows].reverse()});
for(const node of topology.nodes) assert.deepEqual(new Map(shuffled.nodes.map(n=>[n.id,n.position])).get(node.id),node.position,`stable position for ${node.id}`);

const front=cylinderPoint(0,10,{...topology.horizon,referenceLongitude:10});
assert.ok(Math.abs(front[0])<1e-9&&Math.abs(front[2]-topology.horizon.radius)<1e-9&&front[1]===topology.horizon.height,'reference longitude faces the default camera');
assert.ok(cylinderPoint(60,0,topology.horizon)[1]>cylinderPoint(-60,0,topology.horizon)[1],'north is higher on the horizon');

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
assert.ok(large.lanRadius<large.horizon.radius,'horizon surrounds the local network');
console.log(`Topology layout, grouping, aggregation, determinism and horizon mapping passed (${locals.length} hosts, ${large.groups.length} plates).`);
