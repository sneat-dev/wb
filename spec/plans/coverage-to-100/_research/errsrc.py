import re,collections,os,sys
exec(open('classify2.py').read().split('cat=collections.Counter()')[0].replace('sys.argv[1]','"src295"').replace('sys.argv[2]','"nightly/profile.cov"'))
names=collections.Counter()
for k,(n,c) in blocks.items():
    if c: continue
    f,rng=k.split(':'); f=f.replace('github.com/sneat-dev/wb/','')
    sl=int(rng.split('.')[0]); L=lines(f)
    head=L[sl-2]
    if not re.search(r'\berr\w*\s*!=\s*nil',head): continue
    s=head
    if not re.search(r':?=',head.split('!=')[0]):
        for j in range(sl-3, max(0,sl-12), -1):
            if re.search(r'\berr\w*\s*:?=',L[j]): s=L[j]; break
    m=re.search(r'=\s*([\w\.]+)\(',s)
    names[m.group(1) if m else '?']+=n
for k,v in names.most_common(45): print(v,k)
