#!/usr/bin/env node
/*
 * runtime_harvest.js <config.json>
 *
 * 参考模板 — 非通用成品。执行前须按目标站点调整 config.json 及脚本内逻辑：
 *   cookies/localStorage、neutralize 字段、stubs 结构、loginUrlPattern、apiPattern
 *
 * Drives a headless browser through an authorized SPA to capture the live API
 * surface (method + url + body) by defeating three client-side gates:
 *   1. render gate    -> inject fake auth state (cookies / localStorage)
 *   2. interceptor gate-> rewrite the "unauthorized" code field to success
 *   3. content gate   -> stub the menu/permission endpoint with a full-feature payload
 *
 * Requires puppeteer-core + a system Chromium.  ( cd scripts && npm install )
 *
 * config.json schema (all fields optional except baseUrl):
 * {
 *   "baseUrl": "https://target/",
 *   "chromium": "/usr/bin/chromium",            // or env CHROMIUM
 *   "cookies":   [{"name":"auth","value":"b64json:{\"id\":1,\"username\":\"admin\"}"}],
 *   "localStorage": {"token":"x","isLogin":"1"},
 *   "neutralize": { "fields":["response_code","code","errno","ret"], "success":0,
 *                   "flags":{"success":true,"message":"ok"} },
 *   "forward": true,                            // forward real req then rewrite code; false = offline stub
 *   "loginUrlPattern": "/login",               // navigations matching this are suppressed as a fallback
 *   "stubs": [ {"match":"permission|menu|role", "body": { ...full-feature menu... }} ],
 *   "routes": ["/dashboard","/device", ...],   // from routes.txt or the forged menu
 *   "apiPattern": "/api/|/rest/|/graphql",      // what counts as an API call to record
 *   "proxy": "http://127.0.0.1:8080",           // optional; also HTTP_PROXY / HTTPS_PROXY
 *   "waitUntil": "domcontentloaded",            // prefer over networkidle2 for large SPAs
 *   "routeTimeout": 12000,
 *   "waitMs": 1200, "perRouteMs": 900, "headless": true
 * }
 */
process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';
const fs = require('fs');
const path = require('path');

let puppeteer;
try { puppeteer = require('puppeteer-core'); }
catch (e) { console.error("[!] run `npm install` in the scripts/ dir first (needs puppeteer-core)"); process.exit(1); }

function loadCfg(p) {
  const c = JSON.parse(fs.readFileSync(p, 'utf8'));
  c.baseUrl || (() => { throw new Error("config.baseUrl required"); })();
  c.chromium = c.chromium || process.env.CHROMIUM || '/usr/bin/chromium';
  c.neutralize = c.neutralize || { fields: ['response_code', 'code', 'errno', 'ret', 'status'], success: 0, flags: { success: true } };
  c.forward = c.forward !== false;
  c.apiPattern = new RegExp(c.apiPattern || '/api/|/rest/|/service/|/graphql|/gateway/');
  c.loginUrlPattern = c.loginUrlPattern || '/login';
  c.routes = c.routes || ['/'];
  c.waitMs = c.waitMs || 1200; c.perRouteMs = c.perRouteMs || 900;
  c.headless = c.headless !== false;
  c.waitUntil = c.waitUntil || 'domcontentloaded';
  c.routeTimeout = c.routeTimeout || 12000;
  c.captureResponses = c.captureResponses !== false; // record response body samples (forward mode)
  c.recordWs = c.recordWs !== false;                 // record WebSocket frames + SSE endpoints
  c.respMax = c.respMax || 600;                       // truncation length for captured bodies
  c.proxy = c.proxy || process.env.HTTP_PROXY || process.env.HTTPS_PROXY || '';
  (c.stubs || []).forEach(s => s._re = new RegExp(s.match));
  return c;
}

function cookieValue(v) {
  if (typeof v === 'string' && v.startsWith('b64json:'))
    return Buffer.from(v.slice(8)).toString('base64');
  if (typeof v === 'string' && v.startsWith('json:'))
    return v.slice(5);
  return v;
}

function neutralize(txt, n) {
  try {
    const j = JSON.parse(txt);
    if (j && typeof j === 'object') {
      for (const f of n.fields) if (f in j) j[f] = n.success;
      Object.assign(j, n.flags || {});
      return JSON.stringify(j);
    }
  } catch (e) {}
  return txt;
}

(async () => {
  const cfg = loadCfg(process.argv[2] || 'config.json');
  const origin = new URL(cfg.baseUrl).origin;
  const host = new URL(cfg.baseUrl).hostname;
  const rec = [];           // {m,u,b,resp,ct}
  const chunks = new Set();
  const ws = [];            // {url,dir,data}  WebSocket frames
  const sse = new Set();    // SSE (text/event-stream) endpoints

  if (cfg.proxy) {
    process.env.HTTP_PROXY = cfg.proxy;
    process.env.HTTPS_PROXY = cfg.proxy;
  }
  const launchArgs = ['--no-sandbox', '--disable-dev-shm-usage', '--ignore-certificate-errors'];
  if (cfg.proxy) launchArgs.push(`--proxy-server=${cfg.proxy}`);
  const browser = await puppeteer.launch({
    executablePath: cfg.chromium, headless: cfg.headless ? 'new' : false,
    args: launchArgs
  });
  const page = await browser.newPage();

  // ---- WebSocket frame capture via CDP (fetch/XHR interception can't see WS) ----
  if (cfg.recordWs) {
    try {
      const cdp = await page.target().createCDPSession();
      await cdp.send('Network.enable');
      const wsUrl = {}; // requestId -> url
      cdp.on('Network.webSocketCreated', e => { wsUrl[e.requestId] = e.url; });
      const onFrame = dir => e => {
        const p = e.response && e.response.payloadData;
        if (p != null) ws.push({ url: (wsUrl[e.requestId] || '').replace(origin, ''), dir, data: String(p).slice(0, cfg.respMax) });
      };
      cdp.on('Network.webSocketFrameSent', onFrame('send'));
      cdp.on('Network.webSocketFrameReceived', onFrame('recv'));
    } catch (e) { console.log('[ws] CDP capture unavailable:', e.message); }
  }

  // inject localStorage on every document
  if (cfg.localStorage) {
    await page.evaluateOnNewDocument((kv) => {
      try { for (const k in kv) localStorage.setItem(k, kv[k]); } catch (e) {}
    }, cfg.localStorage);
  }
  // fallback: block full-page navigations to the login url
  await page.evaluateOnNewDocument((pat) => {
    const bad = u => { try { return String(u).indexOf(pat) >= 0; } catch (e) { return false; } };
    try {
      const d = Object.getOwnPropertyDescriptor(Location.prototype, 'href');
      Object.defineProperty(Location.prototype, 'href', { configurable: true,
        get() { return d.get.call(this); },
        set(v) { if (bad(v)) return; return d.set.call(this, v); } });
      const a = Location.prototype.assign, r = Location.prototype.replace;
      Location.prototype.assign = function (v) { if (bad(v)) return; return a.call(this, v); };
      Location.prototype.replace = function (v) { if (bad(v)) return; return r.call(this, v); };
    } catch (e) {}
  }, cfg.loginUrlPattern);

  await page.setRequestInterception(true);
  page.on('request', async (req) => {
    const u = req.url(), m = req.method();
    if (/\.js(\?|$)/.test(u) && /\/assets\/|\/static\/|\/js\//.test(u)) chunks.add(u.split('/').pop());

    // suppress fallback login navigations (after the app has bootstrapped)
    if (req.isNavigationRequest() && req.frame() === page.mainFrame()
        && u.includes(cfg.loginUrlPattern) && rec.length > 3) {
      return req.respond({ status: 204, body: '' });
    }
    if (!cfg.apiPattern.test(u)) return req.continue();

    const entry = { m, u: u.replace(origin, '').split('?')[0], full: u, b: req.postData() ? req.postData().slice(0, 400) : null };
    rec.push(entry);

    // explicit stubs (menu / permission forgery) win
    const stub = (cfg.stubs || []).find(s => s._re.test(u));
    if (stub) return req.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(stub.body) });

    // SSE: forwarding a text/event-stream would hang the handler — record + short-circuit
    const accept = (req.headers().accept || '');
    if (/text\/event-stream/.test(accept)) { sse.add(entry.u); return req.respond({ status: 200, contentType: 'application/json', body: '{}' }); }

    if (!cfg.forward) {
      return req.respond({ status: 200, contentType: 'application/json',
        body: neutralize('{"data":{},"list":[],"total":0}', cfg.neutralize) });
    }
    // forward real request, then rewrite the unauthorized code field
    try {
      const headers = Object.assign({}, req.headers());
      if (cfg.cookies) headers.cookie = cfg.cookies.map(c => `${c.name}=${cookieValue(c.value)}`).join('; ');
      const r = await fetch(u, { method: m, headers, body: (m !== 'GET' && m !== 'HEAD') ? req.postData() : undefined });
      const t = await r.text();
      if (cfg.captureResponses) { entry.resp = t.slice(0, cfg.respMax); entry.ct = r.headers.get('content-type') || ''; }
      req.respond({ status: 200, contentType: 'application/json', body: neutralize(t, cfg.neutralize) });
    } catch (e) {
      req.respond({ status: 200, contentType: 'application/json', body: '{"response_code":0,"code":0,"data":{}}' });
    }
  });

  // set auth cookies
  if (cfg.cookies) {
    for (const c of cfg.cookies)
      await page.setCookie({ name: c.name, value: cookieValue(c.value), domain: host, path: c.path || '/' });
  }

  // boot
  await page.goto(cfg.baseUrl, { waitUntil: cfg.waitUntil, timeout: 45000 }).catch(e => console.log('[goto]', e.message));
  await new Promise(r => setTimeout(r, cfg.waitMs));
  const shell = await page.evaluate(() => ({
    url: location.href,
    loginForm: !!document.querySelector('input[type=password]'),
    links: [...document.querySelectorAll('a[href^="/"]')].map(a => a.getAttribute('href'))
  }));
  console.log(`[*] after boot: url=${shell.url}  loginForm=${shell.loginForm}`);
  if (shell.loginForm) console.log('[!] still on login — recheck render-gate facts (cookies/localStorage) in config');

  // discovered menu links extend the route list
  const routes = [...new Set([...cfg.routes, ...shell.links.filter(h => h && h.length > 1)])];
  const perRoute = {};
  for (const rt of routes) {
    const before = rec.length;
    try { await page.goto(origin + rt, { waitUntil: cfg.waitUntil, timeout: cfg.routeTimeout }); } catch (e) {}
    await new Promise(r => setTimeout(r, cfg.perRouteMs));
    const fresh = [...new Set(rec.slice(before).map(r => r.m + ' ' + r.u))];
    perRoute[rt] = fresh;
  }

  const uniq = [...new Set(rec.map(r => r.m + ' ' + r.u))].sort();
  const wsUniq = [...new Set(ws.map(f => f.url))].filter(Boolean).sort();
  const outdir = path.dirname(process.argv[2] || '.');
  fs.writeFileSync(path.join(outdir, 'runtime_api.json'),
    JSON.stringify({ shell, uniq, perRoute, rec, ws, wsEndpoints: wsUniq, sse: [...sse] }, null, 2));

  // merge with static
  let merged = new Set(uniq.map(x => x.split(' ')[1]));
  const staticFile = path.join(outdir, 'api_static.txt');
  if (fs.existsSync(staticFile)) fs.readFileSync(staticFile, 'utf8').split('\n').filter(Boolean).forEach(p => merged.add(p));
  fs.writeFileSync(path.join(outdir, 'api_merged.txt'), [...merged].sort().join('\n'));

  console.log(`[+] runtime endpoints (with method): ${uniq.length}`);
  console.log(`[+] chunks seen at runtime: ${chunks.size}`);
  if (cfg.recordWs) console.log(`[+] websocket endpoints: ${wsUniq.length} (${ws.length} frames) | SSE endpoints: ${sse.size}`);
  if (cfg.captureResponses) console.log(`[+] response bodies captured for ${rec.filter(r => r.resp != null).length}/${rec.length} calls`);
  console.log(`[+] merged unique paths: ${merged.size}  -> api_merged.txt`);
  console.log(`[+] full detail -> runtime_api.json (per-route + bodies + ws frames + sse)`);
  await browser.close();
})();
