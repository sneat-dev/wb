import sys, re, collections, os
S=sys.argv[1]; src=sys.argv[2]; prof=sys.argv[3]
blocks={}
for line in open(prof):
    if line.startswith('mode:'): continue
    k,n,c=line.rsplit(' ',2); n=int(n); c=int(c)
    blocks[k]=(n, (blocks.get(k,(0,False))[1]) or c>0)
files=collections.defaultdict(list)
for k,(n,c) in blocks.items():
    f,rng=k.split(':'); a,b=rng.split(',')
    sl,sc=map(int,a.split('.')); el,ec=map(int,b.split('.'))
    files[f.replace('github.com/sneat-dev/wb/','')].append((sl,sc,el,ec,n,c))
cache={}
def lines(f):
    if f not in cache: cache[f]=open(os.path.join(src,f)).read().split('\n')
    return cache[f]
# function ranges
def funcs(f):
    L=lines(f); out=[]; cur=None
    for i,l in enumerate(L,1):
        m=re.match(r'^func (\([^)]*\) )?([A-Za-z0-9_]+)',l)
        if m: cur=(m.group(2) if not m.group(1) else re.sub(r'.*\*?\b([A-Za-z0-9_]+)\)\s*$',r'\1',m.group(1).strip()[:-1])+'.'+m.group(2), i)
        if l=='}' and cur: out.append((cur[0],cur[1],i)); cur=None
    return out
fnstat=collections.defaultdict(lambda:[0,0])
cat=collections.Counter(); catex=collections.defaultdict(list)
wholefn=collections.Counter()
for f,bl in files.items():
    L=lines(f); FR=funcs(f)
    def fnof(line):
        for name,a,b in FR:
            if a<=line<=b: return name
        return '?'
    for sl,sc,el,ec,n,c in bl:
        fn=fnof(sl); fnstat[(f,fn)][0]+=n
        if c: fnstat[(f,fn)][1]+=n; continue
        head=L[sl-1]; body='\n'.join(L[sl-1:el]); prev='\n'.join(L[max(0,sl-5):sl-1])
        ctx=prev+'\n'+head
        base=os.path.basename(f)
        if re.search(r'_(linux|unix|darwin|windows|bsd)\.go$|_unix|_linux|_darwin',base): k='platform-specific file'
        elif re.search(r'os\.Exit|log\.Fatal|\bFatal\(|panic\(',body): k='os.Exit/log.Fatal/panic'
        elif re.search(r'signal\.|syscall\.SIG|systemd|launchd|launchctl|systemctl|setsid|Setpgid|Kill\(|process\.|FindProcess',ctx+body): k='process/daemon/signal/systemd'
        elif re.search(r'isatty|IsTerminal|term\.|tea\.|bubbletea|lipgloss|huh\.|Stdin|interactive|Interactive|prompt',ctx+body,re.I) : k='TTY/interactive UI'
        elif re.search(r'time\.Sleep|time\.After|NewTicker|NewTimer|backoff|retry|Retry|deadline|Deadline|ctx\.Done\(\)|ctx\.Err\(\)|context\.(Canceled|DeadlineExceeded)',ctx+body): k='time/retry/ctx cancellation'
        elif re.search(r'err\s*!=\s*nil|\berr\b.*:=|if err',head) or re.search(r'^\s*return .*err',body,re.M) and 'err' in head:
            if re.search(r'exec\.|Command\(|runGit|git\(|gitOutput|\.Git\(|Run\(|Output\(|CombinedOutput|\bgh\b|runner\.|Runner|git[A-Z]\w*\(',ctx): k='err branch after git/gh/exec'
            elif re.search(r'http\.|client\.|Client\.|\.Do\(|api\.|API|github|GitHub|connect\.',ctx): k='err branch after network/API'
            elif re.search(r'json\.|yaml\.|toml\.|Marshal|Unmarshal|Encode|Decode|proto\.',ctx): k='err branch after (un)marshal'
            elif re.search(r'os\.|ioutil|filepath\.|ReadFile|WriteFile|MkdirAll|Rename|Remove|Open|Stat|Lstat|Readlink|flock|Lock\(',ctx): k='err branch after fs/OS'
            else: k='err branch after internal call'
        elif re.search(r'\bgo func|go [a-z]\w*\(|sync\.|chan\b|<-',ctx+body): k='goroutine/concurrency'
        else: k='plain logic/validation branch'
        cat[k]+=n; catex[k].append(f"{f}:{sl}")
T=sum(cat.values())
print("UNCOVERED by category (heuristic):")
for k,v in cat.most_common(): print(f"{v:6d} {100*v/T:5.1f}% {k}")
# function level
rows=sorted(fnstat.items(), key=lambda kv: -(kv[1][0]-kv[1][1]))
z=[(k,v) for k,v in fnstat.items() if v[1]==0]
print(f"\nfunctions total={len(fnstat)} zero-covered={len(z)} stmts_in_zero_fns={sum(v[0] for k,v in z)}")
print("\nTop 30 functions by uncovered statements:")
for (f,fn),(n,c) in rows[:30]: print(f"{n-c:5d}/{n:5d} {f} {fn}")
print("\nLargest functions by statements:")
for (f,fn),(n,c) in sorted(fnstat.items(), key=lambda kv:-kv[1][0])[:15]: print(f"{n:5d} {100*c/n:5.1f}% {f} {fn}")
import json
json.dump({k:v[:40] for k,v in catex.items()}, open(os.path.join(S,'cat_examples.json'),'w'), indent=1)
