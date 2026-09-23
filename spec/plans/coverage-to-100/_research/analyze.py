import sys, collections, os
prof=sys.argv[1]
blocks={}
for line in open(prof):
    if line.startswith('mode:'): continue
    k,n,c=line.rsplit(' ',2)
    n=int(n); c=int(c)
    if k in blocks: blocks[k]=(n, blocks[k][1] or c>0)
    else: blocks[k]=(n,c>0)
pkg=collections.defaultdict(lambda:[0,0]); fil=collections.defaultdict(lambda:[0,0])
for k,(n,c) in blocks.items():
    f=k.split(':')[0]; p=os.path.dirname(f).replace('github.com/sneat-dev/wb/','')
    pkg[p][0]+=n; fil[f.replace('github.com/sneat-dev/wb/','')][0]+=n
    if c: pkg[p][1]+=n; fil[f.replace('github.com/sneat-dev/wb/','')][1]+=n
T=sum(v[0] for v in pkg.values()); C=sum(v[1] for v in pkg.values())
print(f"TOTAL stmts={T} covered={C} uncovered={T-C} pct={100*C/T:.2f} packages={len(pkg)} files={len(fil)}")
mode=sys.argv[2] if len(sys.argv)>2 else 'pkg'
d = pkg if mode=='pkg' else fil
rows=sorted(d.items(), key=lambda kv: -(kv[1][0]-kv[1][1]))
lim=int(sys.argv[3]) if len(sys.argv)>3 else 30
for p,(n,c) in rows[:lim]:
    print(f"{n-c:6d} {n:6d} {100*c/n if n else 100:6.1f}% {p}")
if mode=='pkg':
    full=[p for p,(n,c) in pkg.items() if n==c]
    print("packages at 100%:",len(full))
    buckets=collections.Counter()
    for p,(n,c) in pkg.items():
        pc=100*c/n if n else 100
        buckets['100' if pc==100 else '95-100' if pc>=95 else '90-95' if pc>=90 else '80-90' if pc>=80 else '<80']+=1
    print(buckets)
