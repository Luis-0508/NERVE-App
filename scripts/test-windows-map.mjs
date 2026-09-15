// Real browser + real API/sensor only. Install test tooling under ignored .nerve:
// npm install --prefix .nerve/browser-tools --no-package-lock playwright@1.63.0
import { chromium } from '../.nerve/browser-tools/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import path from 'node:path';
const fixture = path.resolve(process.argv[2] ?? '.nerve/windows capture e2e');
const env = await fs.readFile(path.join(fixture,'.env'),'utf8');
const port = env.match(/^NERVE_WEB_PORT=(\d+)/m)?.[1];
const account = JSON.parse(await fs.readFile(path.join(fixture,'test-account.json'),'utf8'));
const browser = await chromium.launch({channel:'msedge',headless:true});
try {
 const context = await browser.newContext({viewport:{width:1440,height:1000}});
 const page = await context.newPage(); const errors=[];
 page.on('pageerror',e=>errors.push(e.message));
 await page.goto(`http://localhost:${port}/login`);
 await page.getByLabel('Email',{exact:true}).fill(account.email);
 await page.getByLabel('Password',{exact:true}).fill(account.password);
 await page.getByRole('button',{name:'Sign in',exact:true}).click();
 await page.waitForURL(url=>!url.pathname.startsWith('/login'));
 await page.goto(`http://localhost:${port}/map`);
 await page.getByText('LIVE TRAFFIC',{exact:true}).waitFor();
 await page.getByText('LOCAL SITE NOT SET',{exact:true}).waitFor();
 // Old values are retained for review, never silently activated.
 await page.evaluate(()=>localStorage.setItem('nerve:traffic-map-origin',JSON.stringify({lat:13.990,lon:-39.849})));
 await page.reload();
 await page.getByText('LOCAL SITE NOT SET',{exact:true}).waitFor();
 await page.getByRole('button',{name:'Local site',exact:true}).click();
 await page.getByText(/older saved location has no source/).waitFor();
 if(await page.getByLabel('Latitude',{exact:true}).inputValue()!=='13.99') throw Error('Legacy candidate lost');
 await page.getByRole('button',{name:'Close location settings',exact:true}).click();
 await page.evaluate(()=>localStorage.setItem('nerve:traffic-map-origin',JSON.stringify({lat:999,lon:0})));
 await page.reload();
 await page.getByText('LOCAL SITE NOT SET',{exact:true}).waitFor();
 await context.grantPermissions(['geolocation']);
 await context.setGeolocation({latitude:53.55,longitude:9.99,accuracy:800});
 // Permission alone must not set a site. Browser button is an explicit action.
 await page.getByRole('button',{name:'Local site',exact:true}).click();
 await page.getByRole('button',{name:'Browser',exact:true}).click();
 await page.getByText(/LOCAL SITE 53\.550.*BROWSER ESTIMATE/).waitFor();
 await page.getByRole('button',{name:'Local site',exact:true}).click();
 // Explicit isolated-browser test input; not a claimed host location/default.
 await page.getByLabel('Latitude',{exact:true}).fill('52.52');
 await page.getByLabel('Longitude',{exact:true}).fill('13.405');
 await page.getByRole('button',{name:'Save location',exact:true}).click();
 await page.getByRole('button',{name:'Diagnostics',exact:true}).click();
 await page.getByText('Windows native sensor',{exact:true}).waitFor();
 await page.getByText('Connected',{exact:true}).waitFor();
 const snapshot=await (await context.request.get(`http://localhost:${port}/api/v1/traffic/live`)).json();
 if(snapshot.source!=='windows-native'||!snapshot.flows.some(f=>f.bytes>0&&f.remoteLatitude!==undefined)) throw Error('Real geolocated native flow missing');
 if(!snapshot.flows.some(f=>f.remoteLocationSource==='dbip-city-lite' && (f.remoteCity||f.remoteRegion))) throw Error('Real city/region enrichment missing');
 await page.getByRole('button',{name:'Close diagnostics',exact:true}).click();
 const canvas=page.locator('.traffic-map-surface canvas');
 await canvas.waitFor();
 await page.waitForTimeout(2000);
 for(const theme of ['dark','light']) {
  for(let i=0;i<3 && await page.locator('html').getAttribute('data-theme')!==theme;i++) await page.getByRole('button',{name:/Theme: .* Change theme\./}).click();
  await page.screenshot({path:path.join(fixture,`real-map-${theme}.png`),fullPage:true});
 }
 await page.reload();
 await page.getByText(/LOCAL SITE 52\.520/).waitFor();
 const saved=await page.evaluate(()=>JSON.parse(localStorage.getItem('nerve:traffic-map-local-site:v2')));
 if(saved.source!=='manual'||saved.version!==2) throw Error('Explicit site provenance lost after reload');
 await page.setViewportSize({width:390,height:844});
 await page.waitForTimeout(2000); // Let the responsive canvas render after reload/resize.
 await page.screenshot({path:path.join(fixture,'real-map-mobile.png'),fullPage:true});
 if(errors.length) throw Error('Browser runtime errors');
 console.log('PASS: real Edge/API flows with City Lite, unset and invalid legacy sites, explicit browser estimate, manual provenance/reload, dark/light/mobile screenshots; no route mocks.');
} finally { await browser.close(); }
