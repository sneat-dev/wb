import sys, glob, os, collections, json
runs = sorted(glob.glob(os.path.join(sys.argv[1], 'run-*', 'profile.cov')))
cov = collections.defaultdict(list)   # block -> list of covered bools
stm = {}
for r in runs:
    seen = {}
    for line in open(r):
        if line.startswith('mode:'): continue
        blk, n, c = line.rsplit(' ', 2)
        seen[blk] = seen.get(blk, False) or int(c) > 0
        stm[blk] = int(n)
    for b, v in seen.items(): cov[b].append(v)
flaky = {b: v for b, v in cov.items() if any(v) and not all(v) and len(v) == len(runs)}
bypkg = collections.defaultdict(list)
for b, v in flaky.items():
    f = b.split(':')[0]
    pkg = os.path.dirname(f).replace('github.com/sneat-dev/wb/', '')
    bypkg[pkg].append((b.replace('github.com/sneat-dev/wb/', ''), sum(v), stm[b]))
print(f"runs={len(runs)} blocks={len(cov)} flaky_blocks={len(flaky)} flaky_statements={sum(stm[b] for b in flaky)}")
for pkg in sorted(bypkg, key=lambda p: -sum(s for _,_,s in bypkg[p])):
    items = sorted(bypkg[pkg])
    print(f"\n{pkg}: {len(items)} blocks, {sum(s for _,_,s in items)} stmts")
    for b, k, s in items: print(f"  {b}  covered {k}/{len(runs)}  ({s} stmt)")
