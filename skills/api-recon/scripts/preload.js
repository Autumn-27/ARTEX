/**
 * api-recon coverage 模式预加载脚本 — 参考模板
 *
 * ⚠ 非通用成品：须按目标站点调整后再注入。
 * 常见改动：loginPathRe、stubs、neutralize.fields/success、apiPattern、mockTier、forward
 *
 * document-start 注入（CDP addScriptToEvaluateOnNewDocument 或 userscript）
 * CONFIG 字段应与 recon/config.json 保持一致
 */
(function () {
  'use strict';

  const CONFIG = {
    loginPathRe: /\/(login|signin)(\/|$|\?)/i,
    mockTier: 'L1+L2',
    forward: true,
    neutralize: {
      fields: ['response_code', 'code', 'errno', 'ret', 'status'],
      success: 0,
      flags: { success: true, message: 'ok' },
    },
    stubs: [],
    apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
    apiFallbackRe: /^\/(api|apis|v\d+|dev|internal)\//i,
    // API 发现增强：fetch/XHR Hook、请求头与响应录制
    recordDetail: true,           // 详细录制 method/url/headers/body/响应
    respMax: 600,
    extractUrlsFromResponse: true, // 从 JSON 响应里抠嵌套 URL
    neutralizeVueRouter: true,    // Vue beforeEach/push 登录跳转中和
    observe: {
      storageReads: false,        // 观察 localStorage.getItem（辅助确认会话键名）
      cookieReads: false,         // 观察 document.cookie 读取
      xhrHeaders: true,           // 录制 XHR setRequestHeader
    },
  };

  window.__API_RECON_PRELOAD__ = true;
  window.__API_RECON_LOG__ = window.__API_RECON_LOG__ || new Set();
  window.__API_RECON_DETAIL__ = window.__API_RECON_DETAIL__ || [];
  window.__API_RECON_ROUTES__ = window.__API_RECON_ROUTES__ || new Set();
  window.__API_RECON_OBSERVE__ = window.__API_RECON_OBSERVE__ || { storage: [], headers: [] };

  function trunc(s, n) {
    n = n || CONFIG.respMax || 600;
    s = String(s == null ? '' : s);
    return s.length > n ? s.slice(0, n) + '…' : s;
  }

  function extractApiUrlsFromText(text) {
    if (!CONFIG.extractUrlsFromResponse || !text) return;
    const re = /["'](\/(?:api|apis|v\d+|dev|internal|graphql)[^"'`\\s]*)["'`]/gi;
    let m;
    while ((m = re.exec(text))) {
      const p = m[1].split('?')[0];
      window.__API_RECON_LOG__.add('GET ' + p);
    }
    const broad = /\/api\/[a-zA-Z0-9_./-]+/g;
    while ((m = broad.exec(text))) {
      const p = m[0].split('?')[0];
      if (!/\.(svg|png|jpg|gif|ico)$/i.test(p)) window.__API_RECON_LOG__.add('GET ' + p);
    }
  }

  function recordApi(url, method) {
    const p = String(url || '').split('?')[0];
    if (CONFIG.apiPattern.test(p)) {
      window.__API_RECON_LOG__.add((method || 'GET').toUpperCase() + ' ' + p);
    }
  }

  function recordApiDetail(entry) {
    recordApi(entry.url, entry.method);
    if (!CONFIG.recordDetail) return;
    window.__API_RECON_DETAIL__.push(entry);
    if (entry.responseBody) extractApiUrlsFromText(entry.responseBody);
  }

  function blocked(url) {
    return url && CONFIG.loginPathRe.test(String(url));
  }

  // --- 跳转中和 ---
  (function neutralizeNativeNavigation() {
    const rawAssign = Location.prototype.assign;
    const rawReplace = Location.prototype.replace;
    Location.prototype.assign = function (url) {
      if (blocked(url)) return;
      return rawAssign.call(this, url);
    };
    Location.prototype.replace = function (url) {
      if (blocked(url)) return;
      return rawReplace.call(this, url);
    };

    const rawPush = history.pushState;
    const rawRep = history.replaceState;
    history.pushState = function (s, t, url) {
      if (blocked(url)) return;
      return rawPush.apply(this, arguments);
    };
    history.replaceState = function (s, t, url) {
      if (blocked(url)) return;
      return rawRep.apply(this, arguments);
    };

    const hrefDesc = Object.getOwnPropertyDescriptor(Location.prototype, 'href');
    if (hrefDesc && hrefDesc.set) {
      const nativeSet = hrefDesc.set;
      Object.defineProperty(Location.prototype, 'href', {
        configurable: true,
        enumerable: hrefDesc.enumerable,
        get: hrefDesc.get,
        set(url) {
          if (blocked(url)) return;
          return nativeSet.call(this, url);
        },
      });
    }
    window.close = function () {};
  })();

  // --- Vue Router 登录跳转中和 ---
  function neutralizeVueRouter() {
    if (!CONFIG.neutralizeVueRouter) return;
    try {
      const el = document.querySelector('[data-v-app]');
      const app = (el && el.__vue_app__) || window.__VUE__;
      if (!app) return;
      const router = app.config && app.config.globalProperties && app.config.globalProperties.$router;
      if (!router) return;
      router.beforeEach(function (to, from, next) { next(); });
      if (router.beforeResolve) router.beforeResolve(function (to, from, next) { next(); });
      function wrapNav(fn) {
        return function (loc) {
          const path = typeof loc === 'string' ? loc : (loc && (loc.path || loc.fullPath)) || '';
          if (blocked(path)) return Promise.resolve();
          return fn.apply(this, arguments);
        };
      }
      router.push = wrapNav(router.push.bind(router));
      router.replace = wrapNav(router.replace.bind(router));
      if (router.getRoutes) {
        router.getRoutes().forEach(function (r) {
          if (r.path) window.__API_RECON_ROUTES__.add(r.path);
        });
      }
    } catch (e) { /* ignore */ }
  }

  function runPostLoadHooks() {
    neutralizeVueRouter();
    // 业务层跳转函数（goPage / navigateTo 等）
    ['goPage', 'navigateTo', 'jumpTo', 'redirectTo'].forEach(function (name) {
      if (typeof window[name] !== 'function' || window[name].__apiReconWrapped) return;
      const raw = window[name];
      window[name] = function () {
        const arg = arguments[0];
        const path = typeof arg === 'string' ? arg : (arg && arg.path) || '';
        if (blocked(path)) return;
        return raw.apply(this, arguments);
      };
      window[name].__apiReconWrapped = true;
    });
  }
  document.addEventListener('DOMContentLoaded', runPostLoadHooks);
  window.addEventListener('load', runPostLoadHooks);

  // --- 观察 Hook：辅助发现会话键名与请求头（可选，默认关 storage/cookie）---
  if (CONFIG.observe && CONFIG.observe.storageReads) {
    const rawGet = Storage.prototype.getItem;
    Storage.prototype.getItem = function (key) {
      window.__API_RECON_OBSERVE__.storage.push({ type: 'getItem', key: key, at: location.pathname });
      return rawGet.apply(this, arguments);
    };
  }
  if (CONFIG.observe && CONFIG.observe.cookieReads) {
    const cookieDesc = Object.getOwnPropertyDescriptor(Document.prototype, 'cookie');
    if (cookieDesc && cookieDesc.get) {
      const nativeGet = cookieDesc.get;
      Object.defineProperty(document, 'cookie', {
        configurable: true,
        get: function () {
          window.__API_RECON_OBSERVE__.storage.push({ type: 'cookieRead', at: location.pathname });
          return nativeGet.call(this);
        },
        set: cookieDesc.set,
      });
    }
  }

  // --- Mock 辅助 ---
  const NEGATIVE_RE = /未登录|未授权|授权|not\s*login|unauthorized|forbidden/i;
  const tier = CONFIG.mockTier || 'L1+L2';

  function patchJsonBody(text) {
    if (!tier.includes('L2')) return text;
    try {
      const j = JSON.parse(text);
      if (j && typeof j === 'object') {
        const msg = String(j.message || j.msg || '');
        for (const f of CONFIG.neutralize.fields) {
          if (f in j && j[f] !== CONFIG.neutralize.success && NEGATIVE_RE.test(msg)) {
            j[f] = CONFIG.neutralize.success;
          }
        }
        Object.assign(j, CONFIG.neutralize.flags || {});
        if (j.data == null) j.data = {};
        return JSON.stringify(j);
      }
    } catch (e) { /* ignore */ }
    return text;
  }

  function lookupPrecise(url) {
    if (!tier.includes('L1')) return null;
    const p = String(url || '');
    for (const s of CONFIG.stubs) {
      if (s._re ? s._re.test(p) : new RegExp(s.match).test(p)) return s.body;
    }
    return null;
  }

  function fallbackBody(url) {
    const p = String(url).split('?')[0];
    if (/\/(list|search|query|page|all|tree|nodes|options)/i.test(p)) {
      return { response_code: 0, data: [] };
    }
    if (/\/(get|detail|info|config|status)/i.test(p)) {
      return { response_code: 0, data: {} };
    }
    return { response_code: 0, data: {}, success: true };
  }

  function matchMock(url) {
    const precise = lookupPrecise(url);
    if (precise) return precise;
    if (tier.includes('L3') && CONFIG.apiFallbackRe.test(String(url || '').split('?')[0])) {
      return fallbackBody(url);
    }
    return null;
  }

  CONFIG.stubs.forEach(function (s) {
    if (s.match && !s._re) s._re = new RegExp(s.match);
  });

  // --- fetch Hook ---
  const rawFetch = window.fetch;
  window.fetch = async function (input, init) {
    const url = typeof input === 'string' ? input : (input && input.url) || '';
    const method = ((init && init.method) || 'GET').toUpperCase();
    const reqHeaders = (init && init.headers) || {};
    const reqBody = init && init.body ? trunc(init.body) : null;

    const mock = matchMock(url);
    if (mock && !CONFIG.forward) {
      recordApiDetail({ method: method, url: url, reqHeaders: reqHeaders, reqBody: reqBody, status: 200, responseBody: JSON.stringify(mock), source: 'mock' });
      return new Response(JSON.stringify(mock), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }

    const resp = await rawFetch.apply(this, arguments);
    const clone = resp.clone();
    let text = '';
    try { text = await clone.text(); } catch (e) { /* ignore */ }

    recordApiDetail({
      method: method, url: url, reqHeaders: reqHeaders, reqBody: reqBody,
      status: resp.status, responseBody: trunc(text), source: 'fetch',
    });

    if (!tier.includes('L2') && !mock) return resp;

    const patched = patchJsonBody(text);
    if (patched !== text) {
      return new Response(patched, {
        status: resp.status,
        statusText: resp.statusText,
        headers: resp.headers,
      });
    }
    return resp;
  };

  // --- XHR Hook ---
  const rawOpen = XMLHttpRequest.prototype.open;
  const rawSend = XMLHttpRequest.prototype.send;
  const rawSetHeader = XMLHttpRequest.prototype.setRequestHeader;

  XMLHttpRequest.prototype.open = function (method, url) {
    this.__apiReconUrl = url;
    this.__apiReconMethod = method;
    this.__apiReconHeaders = {};
    return rawOpen.apply(this, arguments);
  };

  if (CONFIG.observe && CONFIG.observe.xhrHeaders) {
    XMLHttpRequest.prototype.setRequestHeader = function (name, value) {
      if (!this.__apiReconHeaders) this.__apiReconHeaders = {};
      this.__apiReconHeaders[name] = value;
      window.__API_RECON_OBSERVE__.headers.push({ name: name, url: this.__apiReconUrl });
      return rawSetHeader.apply(this, arguments);
    };
  }

  XMLHttpRequest.prototype.send = function (body) {
    const xhr = this;
    const url = xhr.__apiReconUrl || '';
    const method = (xhr.__apiReconMethod || 'GET').toUpperCase();
    const mock = matchMock(url);

    if (mock && !CONFIG.forward) {
      const bodyStr = JSON.stringify(mock);
      recordApiDetail({ method: method, url: url, reqHeaders: xhr.__apiReconHeaders, reqBody: trunc(body), status: 200, responseBody: bodyStr, source: 'mock' });
      setTimeout(function () {
        Object.defineProperty(xhr, 'readyState', { configurable: true, get: function () { return 4; } });
        Object.defineProperty(xhr, 'status', { configurable: true, get: function () { return 200; } });
        Object.defineProperty(xhr, 'responseText', { configurable: true, get: function () { return bodyStr; } });
        xhr.onreadystatechange && xhr.onreadystatechange();
        xhr.onload && xhr.onload();
      }, 0);
      return;
    }

    const orig = xhr.onreadystatechange;
    xhr.onreadystatechange = function () {
      if (xhr.readyState === 4) {
        recordApiDetail({
          method: method, url: url, reqHeaders: xhr.__apiReconHeaders,
          reqBody: trunc(body), status: xhr.status,
          responseBody: trunc(xhr.responseText), source: 'xhr',
        });
        if (tier.includes('L2') && xhr.responseText) {
          try {
            const patched = patchJsonBody(xhr.responseText);
            if (patched !== xhr.responseText) {
              Object.defineProperty(xhr, 'responseText', { configurable: true, get: function () { return patched; } });
            }
          } catch (e) { /* ignore */ }
        }
      }
      orig && orig.apply(this, arguments);
    };
    return rawSend.apply(this, arguments);
  };
})();
