#!/usr/bin/env python3
"""
extract_route_map.py <JSDIR> <OUTDIR>

Scan downloaded JS chunks for routeMap / routeLink style objects:
  KEY:{name:"...",link:"/path"}

Writes route_map.json — used by build_perm_tree.py to resolve alias refs.
"""
import re, json, os, sys, glob

def main():
    if len(sys.argv) < 3:
        print("usage: extract_route_map.py <JSDIR> <OUTDIR>")
        sys.exit(1)
    jsdir, outdir = sys.argv[1], sys.argv[2]
    os.makedirs(outdir, exist_ok=True)

    best = {}
    best_file = None
    pat = re.compile(r'([A-Z_][A-Z0-9_]*):\{name:"([^"]*)",link:"([^"]+)"')

    for fp in glob.glob(os.path.join(jsdir, '*.js')):
        try:
            txt = open(fp, encoding='utf-8', errors='ignore').read()
        except Exception:
            continue
        hits = pat.findall(txt)
        if len(hits) > len(best):
            best = {k: {'name': n, 'link': l} for k, n, l in hits}
            best_file = fp

    if not best:
        print('[!] no routeMap pattern found — widen regex or grep manually')
        sys.exit(1)

    out = os.path.join(outdir, 'route_map.json')
    json.dump(best, open(out, 'w', encoding='utf-8'), ensure_ascii=False, indent=2)
    print(f'[+] {len(best)} routes from {os.path.basename(best_file)} -> {out}')

if __name__ == '__main__':
    main()
