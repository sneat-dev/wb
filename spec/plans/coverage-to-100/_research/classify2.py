import sys, re, collections, os
src=sys.argv[1]; prof=sys.argv[2]
blocks={}
for line in open(prof):
    if line.startswith('mode:'): continue
    k,n,c=line.rsplit(' ',2); n=int(n); c=int(c)
    blocks[k]=(n, (blocks.get(k,(0,False))[1]) or c>0)
cache={}
def lines(f):
    if f not in cache: cache[f]=open(os.path.join(src,f)).read().split('\n')
    return cache[f]
cat=collections.Counter(); sub=collections.Counter(); pkgcat=collections.defaultdict(collections.Counter)
for k,(n,c) in blocks.items():
    if c: continue
    f,rng=k.split(':'); f=f.replace('github.com/sneat-dev/wb/','')
    sl=int(rng.split('.')[0]); el=int(rng.split(',')[1].split('.')[0])
    L=lines(f); head=L[sl-2] if L[sl-2].rstrip().endswith("{") or L[sl-2].rstrip().endswith(":") else L[sl-1]; body='\n'.join(L[sl-1:el])
    pkg=os.path.dirname(f)
    base=os.path.basename(f)
    if re.search(r'_(linux|unix|darwin|windows|freebsd|bsd|other|notwindows)\.go$',base):
        k1='platform-specific file'
    elif re.search(r'\berr\w*\s*!=\s*nil',head) or re.search(r'\bErr\w*\s*!=\s*nil',head):
        # find err source
        srcline=head
        if not re.search(r':?=',head.split('!=')[0]) :
            for j in range(sl-2, max(0,sl-12), -1):
                if re.search(r'\berr\w*\s*:?=',L[j]): srcline=L[j]; break
        s=srcline
        if re.search(r'exec\.|Command|[Gg]it\w*\(|\bgh\w*\(|[Rr]unner|\.Run\(|\.Output\(|CombinedOutput|runCommand|run\(',s): k1='error propagation: git/gh/exec call'
        elif re.search(r'http|[Cc]lient\.|\.Do\(|github|GitHub|connect\.|Fetch|API',s): k1='error propagation: network/GitHub API'
        elif re.search(r'json\.|yaml\.|toml\.|Marshal|Unmarshal|Encode|Decode|proto\.|hcl',s): k1='error propagation: (un)marshal/encode'
        elif re.search(r'\bos\.|filepath\.|ioutil|io\.|ReadFile|WriteFile|MkdirAll|Rename|Remove|Open|Stat|Lstat|Readlink|flock|Lock|Chmod|Symlink|Getwd|UserHomeDir|Executable|fsync|Sync\(|Close\(',s): k1='error propagation: filesystem/OS'
        else: k1='error propagation: internal function'
    elif re.search(r'os\.Exit|log\.Fatal|panic\(',body): k1='os.Exit/log.Fatal/panic'
    elif re.search(r'time\.Sleep|time\.After|Ticker|Timer|ctx\.Done|ctx\.Err|Deadline|deadline|timeout|Timeout|retry|attempt',head+body): k1='time/retry/timeout/ctx'
    elif re.search(r'go func|\bgo \w+\(|<-|select \{',head+body): k1='goroutine/concurrency'
    elif re.search(r'IsTerminal|isatty|tea\.|interactive|Interactive|confirm|prompt',head+body): k1='TTY/interactive'
    elif re.search(r'^\s*return[^\n]*(Errorf|errors\.New|exitError|Refus|refus|Err[A-Z]\w*)',body,re.M) or re.search(r'Errorf|errors\.New|&exitError',body.split('\n')[1] if body.count('\n')>=1 else body): k1='refusal/validation error return'
    elif re.search(r'^\s*default:|^\s*case ',head): k1='switch case/default'
    else: k1='other behaviour (untested path)'
    cat[k1]+=n; pkgcat[pkg if pkg in ('cmd/wb','internal/worktrees','internal/orchestrate') else 'long tail (94 pkgs)'][k1]+=n
T=sum(cat.values())
for k,v in cat.most_common(): print(f"{v:6d} {100*v/T:5.1f}% {k}")
print()
for p,cc in pkgcat.items():
    t=sum(cc.values()); print(p,t, '; '.join(f"{k.replace('error propagation','errprop')}={v}" for k,v in cc.most_common(6)))
