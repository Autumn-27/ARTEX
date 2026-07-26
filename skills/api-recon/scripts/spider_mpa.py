#!/usr/bin/env python3
"""
spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "k=v; k2=v2"] [--max 200] [--depth 4]

参考模板 — 非通用成品。执行前须按目标调整 --exclude、cookie、depth/max 等同域策略。

Fallback for NON-SPA targets (traditional server-rendered MPAs: Django/Rails/PHP/
JSP, classic admin panels). When there is no JS endpoint bundle, the API surface
lives in HTML <form action>, <a href>, and inline-JS ajax urls. This BFS-crawls
the same origin and extracts:
  - forms.txt : METHOD action  [param1, param2, ...]   (the real "endpoints")
  - links.txt : every same-origin URL reached
  - api_inline.txt : url-ish strings found in inline <script> / onclick (fetch/ajax)

Stdlib only. Same-origin, bounded, polite. Provide --cookie for an authed crawl.
"""
import sys, os, re, ssl, argparse, urllib.request, urllib.parse, time
from html.parser import HTMLParser
from collections import deque

CTX = ssl.create_default_context(); CTX.check_hostname = False; CTX.verify_mode = ssl.CERT_NONE
UA = "Mozilla/5.0 (spa-api-recon spider)"
SKIP_EXT = re.compile(r'\.(css|png|jpe?g|gif|svg|ico|woff2?|ttf|pdf|zip|mp4|webp|js)(\?|$)', re.I)
API_HINT = re.compile(r'/(?:api|rest|service|ajax|action|do|rpc|graphql|v\d|admin|backend)\b', re.I)

def fetch(url, cookie):
    h = {"User-Agent": UA}
    if cookie: h["Cookie"] = cookie
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=h), context=CTX, timeout=20) as r:
            ct = r.headers.get("Content-Type", "")
            if "html" not in ct and "xml" not in ct: return "", ct
            return r.read().decode("utf-8", "ignore"), ct
    except Exception:
        return "", ""

class Page(HTMLParser):
    def __init__(self):
        super().__init__()
        self.links, self.scripts, self.inline = [], [], []
        self.forms, self._cur = [], None
        self._in_script = False
    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "a" and a.get("href"): self.links.append(a["href"])
        elif tag == "script":
            self._in_script = True
            if a.get("src"): self.scripts.append(a["src"])
        elif tag == "form":
            self._cur = {"action": a.get("action", ""), "method": (a.get("method") or "GET").upper(), "params": []}
        elif tag in ("input", "select", "textarea", "button") and self._cur is not None:
            n = a.get("name")
            if n: self._cur["params"].append(n)
        # ajax-ish handlers
        for v in a.values():
            for m in re.findall(r'''["'](/[^"']{2,120})["']''', v or ""):
                if API_HINT.search(m): self.inline.append(m)
    def handle_endtag(self, tag):
        if tag == "script": self._in_script = False
        elif tag == "form" and self._cur is not None:
            self.forms.append(self._cur); self._cur = None
    def handle_data(self, data):
        if self._in_script and data:
            for m in re.findall(r'''["'`](/[A-Za-z0-9_\-./{}$:?=&]{2,120})["'`]''', data):
                if API_HINT.search(m): self.inline.append(m)

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("base"); ap.add_argument("outdir")
    ap.add_argument("--cookie", default=""); ap.add_argument("--max", type=int, default=200)
    ap.add_argument("--depth", type=int, default=4); ap.add_argument("--delay", type=float, default=0.1)
    ap.add_argument("--include", default=""); ap.add_argument("--exclude", default="logout|signout|delete|remove|destroy")
    args = ap.parse_args()
    base = args.base if args.base.startswith("http") else "https://" + args.base
    os.makedirs(args.outdir, exist_ok=True)
    origin = "{0.scheme}://{0.netloc}".format(urllib.parse.urlparse(base))
    inc = re.compile(args.include) if args.include else None
    exc = re.compile(args.exclude, re.I) if args.exclude else None

    seen, forms, inline, links = set(), {}, set(), set()
    q = deque([(base, 0)]); seen.add(base.split("#")[0])
    n = 0
    while q and n < args.max:
        url, d = q.popleft(); n += 1
        html, ct = fetch(url, args.cookie)
        if args.delay: time.sleep(args.delay)
        if not html: continue
        p = Page()
        try: p.feed(html)
        except Exception: pass
        links.add(url.replace(origin, "") or "/")
        for f in p.forms:
            act = urllib.parse.urljoin(url, f["action"] or url)
            key = f["method"] + " " + act.replace(origin, "")
            forms.setdefault(key, set()).update(f["params"])
        for s in p.inline: inline.add(s)
        if d < args.depth:
            for href in p.links:
                if href.startswith(("mailto:", "tel:", "javascript:", "#")): continue
                nxt = urllib.parse.urljoin(url, href).split("#")[0]
                if not nxt.startswith(origin): continue
                if SKIP_EXT.search(nxt): continue
                if exc and exc.search(nxt): continue
                if inc and not inc.search(nxt): continue
                if nxt not in seen:
                    seen.add(nxt); q.append((nxt, d + 1))

    with open(os.path.join(args.outdir, "forms.txt"), "w") as fh:
        for k in sorted(forms):
            ps = ", ".join(sorted(forms[k]))
            fh.write(f"{k}  [{ps}]\n")
    open(os.path.join(args.outdir, "links.txt"), "w").write("\n".join(sorted(links)))
    open(os.path.join(args.outdir, "api_inline.txt"), "w").write("\n".join(sorted(inline)))

    print(f"[+] crawled {n} pages (cap {args.max}, depth {args.depth})")
    print(f"[+] forms (endpoints): {len(forms)}  -> forms.txt")
    print(f"[+] inline ajax urls:  {len(inline)} -> api_inline.txt")
    print(f"[+] pages reached:     {len(links)} -> links.txt")
    if not args.cookie:
        print("[i] no --cookie: only public pages crawled. Pass an authed session cookie for the full surface.")

if __name__ == "__main__":
    main()
