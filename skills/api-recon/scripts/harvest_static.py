#!/usr/bin/env python3
"""
harvest_static.py <BASE_URL> <OUTDIR>

参考模板 — 非通用成品。执行前须按目标站点调整，常见改动：
  - extract_endpoints 正则（endpoint 方言）
  - webpack/Vite manifest 解析逻辑
  - 微前端 publicPath、重试策略

Static SPA bundle harvester. Framework-agnostic; tuned for webpack + Vite.
  1. Fetch entry HTML, collect script/module references.
  2. Parse the runtime chunk manifest(s) and download EVERY chunk (not just the
     ones in HTML), looping until no new chunk ids appear. Handles multiple
     micro-frontend runtimes, each with its own publicPath.
  3. Extract API endpoints and route paths from all downloaded JS.

Stdlib only. TLS verification is disabled (recon against self-signed/internal hosts).
"""
import sys, os, re, ssl, json, urllib.request, urllib.parse
from concurrent.futures import ThreadPoolExecutor

CTX = ssl.create_default_context(); CTX.check_hostname = False; CTX.verify_mode = ssl.CERT_NONE
UA = "Mozilla/5.0 (spa-api-recon)"

def fetch(url, binary=False):
    try:
        req = urllib.request.Request(url, headers={"User-Agent": UA})
        with urllib.request.urlopen(req, context=CTX, timeout=25) as r:
            data = r.read()
            return (data if binary else data.decode("utf-8", "ignore")), r.status
    except Exception as e:
        code = getattr(e, "code", 0)
        return "", code

def origin_of(u):
    p = urllib.parse.urlparse(u)
    return f"{p.scheme}://{p.netloc}"

# ---- chunk manifest parsing -------------------------------------------------
def matched_block(s, end_brace_idx):
    """Walk back from a '}' index to its matching '{' and return the object body."""
    depth = 0; j = end_brace_idx
    while j >= 0:
        if s[j] == '}': depth += 1
        elif s[j] == '{':
            depth -= 1
            if depth == 0: return s[j:end_brace_idx+1]
        j -= 1
    return ""

def parse_chunk_maps(js_text):
    """Return list of (publicPath_hint, {id: hash}) for every `}[x]+".js"` map
    (webpack __webpack_require__.u) found in the text."""
    maps = []
    # publicPath hints in this file: .p="..."  or publicPath="..."
    pubs = re.findall(r'(?:\.p|publicPath)\s*=\s*"([^"]*)"', js_text)
    for m in re.finditer(r'\}\[[A-Za-z_$]\]\s*\+\s*"\.js"', js_text):
        body = matched_block(js_text, m.start())
        pairs = re.findall(r'(\d+):"([0-9a-fA-F]{6,16})"', body)
        if pairs:
            maps.append((pubs, dict(pairs)))
    # Vite style: __vite__mapDeps map of "assets/xx.js"
    for fn in re.findall(r'"(assets/[^"]+\.js)"', js_text):
        maps.append((["/"], {"__vite__": fn}))
    return maps

def chunk_urls(base, js_text):
    """Yield absolute chunk URLs reconstructable from this file's manifest(s)."""
    origin = origin_of(base)
    out = set()
    for pubs, idmap in parse_chunk_maps(js_text):
        paths = pubs or ["/assets/", "/"]
        for cid, h in idmap.items():
            if cid == "__vite__":
                fn = h  # already "assets/xx.js"
                for pp in paths:
                    out.add(urllib.parse.urljoin(origin + "/", fn))
                continue
            for pp in paths:
                if pp.startswith("http"): bareorigin = ""; prefix = pp
                else: bareorigin = origin; prefix = pp if pp.startswith("/") else "/"+pp
                if not prefix.endswith("/"): prefix += "/"
                out.add(f"{bareorigin}{prefix}{cid}.{h}.js")
    return out

# ---- endpoint / route extraction -------------------------------------------
API_HINT = re.compile(r'/(?:api|rest|service|services|gateway|graphql|v\d|web|admin|backend|open)\b', re.I)
ASSET_EXT = re.compile(r'\.(js|css|png|jpe?g|svg|gif|woff2?|ttf|ico|map|json|mp4|webp)(\?|$)', re.I)

def extract_endpoints(js_text):
    paths = set()
    # quoted ('/...'), double-quoted, and backtick template paths
    for m in re.findall(r'''["'`](/[A-Za-z0-9_\-./{}$:]+)["'`]''', js_text):
        paths.add(m)
    # concatenation heads:  "/api/x/" + var
    for m in re.findall(r'''["'](/[A-Za-z0-9_\-./]+/)["']\s*\+''', js_text):
        paths.add(m)
    api, other = set(), set()
    for p in paths:
        if ASSET_EXT.search(p): continue
        if p.count('/') < 2 and not API_HINT.search(p): continue
        (api if API_HINT.search(p) else other).add(p)
    return api, other

def extract_routes(js_text):
    r = set()
    for key in ('path', 'to', 'redirect', 'href'):
        for m in re.findall(key + r'''\s*:\s*["'](/[A-Za-z0-9_\-/:]*)["']''', js_text):
            if not ASSET_EXT.search(m) and not API_HINT.search(m):
                r.add(m)
    return r

# ---- main -------------------------------------------------------------------
def main():
    if len(sys.argv) < 3:
        print("usage: harvest_static.py <BASE_URL> <OUTDIR>"); sys.exit(1)
    base, outdir = sys.argv[1], sys.argv[2]
    if not base.startswith("http"): base = "https://" + base
    jsdir = os.path.join(outdir, "js"); os.makedirs(jsdir, exist_ok=True)

    print(f"[*] entry: {base}")
    html, status = fetch(base)
    open(os.path.join(outdir, "index.html"), "w").write(html)
    origin = origin_of(base)

    # initial scripts from HTML
    srcs = set(re.findall(r'<script[^>]+src="([^"]+\.js[^"]*)"', html))
    srcs |= set(re.findall(r'(?:src|href)="([^"]*\.js)"', html))
    seed = set()
    for s in srcs:
        seed.add(s if s.startswith("http") else urllib.parse.urljoin(base, s))
    print(f"[*] {len(seed)} scripts referenced in HTML")

    have = {}  # url -> local path
    def dl(url):
        fn = os.path.basename(urllib.parse.urlparse(url).path)
        if not fn.endswith(".js"): return None
        lp = os.path.join(jsdir, fn)
        if url in have: return have[url]
        data, st = fetch(url, binary=True)
        if st == 200 and data and not data[:15].lstrip().startswith(b"<"):
            open(lp, "wb").write(data); have[url] = lp; return lp
        return None

    with ThreadPoolExecutor(max_workers=20) as ex:
        list(ex.map(dl, seed))

    # iteratively expand via chunk manifests (chunks reference more chunks)
    seen_urls = set(have.keys()); frontier = list(have.values())
    rounds = 0
    while frontier and rounds < 6:
        rounds += 1
        new_urls = set()
        for lp in frontier:
            try: txt = open(lp, encoding="utf-8", errors="ignore").read()
            except: continue
            for cu in chunk_urls(base, txt):
                if cu not in seen_urls: new_urls.add(cu)
        seen_urls |= new_urls
        if not new_urls: break
        print(f"[*] round {rounds}: {len(new_urls)} new chunk urls from manifest")
        before = set(have.values())
        with ThreadPoolExecutor(max_workers=24) as ex:
            list(ex.map(dl, new_urls))
        frontier = [p for p in have.values() if p not in before]

    # retry-once any manifest chunk that 404'd (transient failures are real)
    all_manifest = set()
    for lp in list(have.values()):
        try: all_manifest |= chunk_urls(base, open(lp, encoding="utf-8", errors="ignore").read())
        except: pass
    missing = [u for u in all_manifest if u not in have]
    if missing:
        with ThreadPoolExecutor(max_workers=24) as ex:
            list(ex.map(dl, missing))
        still = [u for u in all_manifest if u not in have]
        print(f"[*] manifest chunks: {len(all_manifest)} | downloaded {len(have)} | "
              f"unreachable {len(still)} (CSS-only / undeployed)")

    print(f"[+] total JS downloaded: {len(have)}")

    # extract from everything
    api, other, routes = set(), set(), set()
    for lp in have.values():
        try: txt = open(lp, encoding="utf-8", errors="ignore").read()
        except: continue
        a, o = extract_endpoints(txt); api |= a; other |= o
        routes |= extract_routes(txt)

    def dump(name, items):
        path = os.path.join(outdir, name)
        open(path, "w").write("\n".join(sorted(items)))
        return path
    dump("api_static.txt", api)
    dump("paths_other.txt", other)
    dump("routes.txt", routes)
    dump("chunkmap.txt", sorted(os.path.basename(u) for u in all_manifest))

    print(f"[+] api endpoints: {len(api)}  (api_static.txt)")
    print(f"[+] other paths:   {len(other)} (paths_other.txt)")
    print(f"[+] route paths:   {len(routes)} (routes.txt)")
    print(f"[+] outdir: {outdir}")
    print("\n[next] reverse the 3 gate facts (see reference.md), fill config.json, "
          "then: node runtime_harvest.js config.json")

if __name__ == "__main__":
    main()
