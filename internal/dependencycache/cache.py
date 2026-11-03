"""WB-owned Linux dependency archives. Plan by default; --apply runs the cache job."""
import argparse
import hashlib
import ast
import fcntl
import json
import os
import pathlib
import posixpath
import shutil
import re
import stat
import subprocess
import sys
import tarfile
import tempfile
import time

def require(condition, message):
    if not condition:
        raise RuntimeError(message)


class BoundedArchive:
    def __init__(self, stream, limit):
        self.stream, self.limit = stream, limit

    def write(self, data):
        require(self.stream.tell() + len(data) <= self.limit, 'Compressed cache bound exceeded')
        return self.stream.write(data)

    def __getattr__(self, name):
        return getattr(self.stream, name)


require(sys.platform == "linux" and os.getuid() != 0, "Linux non-root execution required")
require(not os.environ.get("DEEPSEEK_API_KEY"), "Provider credential present")
require(not os.environ.get("WB_AGENT_PROVIDER_CREDENTIAL"), "Provider credential present")
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--apply', action='store_true')
parser.add_argument('--image')
parser.add_argument('--source-manifest')
parser.add_argument('--max-bytes', type=int, default=2 * 1024**3)
parser.add_argument('--max-age-seconds', type=int, default=7 * 86400)
parser.add_argument('--min-free-bytes', type=int, default=512 * 1024**2)
parser.add_argument('--growth-bytes', type=int, default=4 * 1024**3)
parser.add_argument('--store-dir', choices=('/cache/pnpm-store', '/tmp/wb-pnpm-store'), default='/cache/pnpm-store')
args = parser.parse_args()
if not all(n > 0 for n in (args.max_bytes, args.max_age_seconds, args.min_free_bytes, args.growth_bytes)):
    parser.error('Positive cache bounds required')
if args.apply and re.fullmatch(r'(?:[a-z0-9./:_-]+@)?sha256:[0-9a-f]{64}', args.image or '') is None:
    parser.error('Exact image digest required with --apply')
root = pathlib.Path.cwd().resolve()
image = args.image
manager = json.loads((root / 'package.json').read_text()).get('packageManager', '')
declared = re.fullmatch(r'pnpm@(\d+\.\d+\.\d+)(?:\+sha512\.[0-9a-f]+)?', manager)
require(declared is not None, 'Exact packageManager pnpm version required')
version = declared[1]
started = time.monotonic()


def workspace_packages():
    workspace = (root / "pnpm-workspace.yaml").read_text()
    require(len(re.findall(r"^packages:", workspace, re.M)) == 1, "One explicit workspace package list required")
    lines = workspace.splitlines()
    headers = [i for i, line in enumerate(lines) if re.fullmatch(r"packages:\s*", line)]
    require(len(headers) == 1, "Unsupported workspace package-list syntax")
    packages = set()
    for line in lines[headers[0] + 1:]:
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if not line[0].isspace():
            break
        require(re.fullmatch(r"[ \t]+-[ \t]+.+", line) is not None, "Unsupported workspace package-list entry")
        package = ast.literal_eval(line.strip()[1:].strip())
        require(isinstance(package, str) and not any(c in package for c in "*?[]{}"), "Explicit workspace paths required")
        relative = pathlib.PurePosixPath(package)
        require(not relative.is_absolute() and ".." not in relative.parts
                and str(relative) == package, "Workspace path leaves worktree")
        packages.add(package)
    return packages


def snapshot_inputs():
    pinned = json.loads(pathlib.Path(args.source_manifest).read_text())['files'] if args.source_manifest else None
    tracked = set(pinned) if pinned is not None else set(subprocess.check_output([
        "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"
    ]).decode().split("\0"))
    inputs = sorted(n for n in tracked if n and (
        n == "package.json" or n.endswith("/package.json")
        or n in ("pnpm-lock.yaml", "pnpm-workspace.yaml")
        or pathlib.PurePosixPath(n).name in (".npmrc", ".pnpmfile.cjs")
        or n.startswith("patches/")))
    for name in (".npmrc", ".pnpmfile.cjs"):
        if (root / name).exists() and name not in inputs:
            inputs.append(name)
    required_masks = {'node_modules'}
    package_manifests = {'package.json'}
    inputs.append('package.json')
    for package in workspace_packages():
        relative = pathlib.PurePosixPath(package)
        package_manifest = str(relative / "package.json")
        inputs.append(package_manifest)
        package_manifests.add(package_manifest)
        required_masks.add(str(relative / "node_modules"))
    require(bool(required_masks), "Workspace package list is empty")
    for name in sorted(package_manifests):
        if name == "package.json" or name.endswith("/package.json"):
            data = json.loads((root / name).read_text())
            require(not any(data.get("scripts", {}).get(k) for k in (
                "preinstall", "install", "postinstall", "prepare")), "Workspace lifecycle inputs need an explicit cache contract")
            for section in ("dependencies", "devDependencies", "optionalDependencies"):
                for reference in data.get(section, {}).values():
                    if isinstance(reference, str) and reference.startswith(("file:", "link:")):
                        target = (root / name).parent / reference.split(":", 1)[1]
                        require(target.is_file() and not target.is_symlink(), "Local directory dependencies need an explicit cache contract")
                        resolved = target.resolve()
                        require(root in resolved.parents, "Local dependency leaves worktree")
                        inputs.append(str(resolved.relative_to(root)))
    inputs = sorted(set(inputs))
    digest = hashlib.sha256()
    for name in sorted(inputs):
        source = root / name
        require(not source.is_symlink() and root in source.resolve().parents, "Dependency input leaves worktree")
        data = source.read_bytes()
        if pinned is not None:
            require(name in pinned and hashlib.sha256(data).hexdigest() == pinned[name]['sha256'],
                    'Dependency input differs from frozen source manifest')
        digest.update(name.encode() + b"\0" + data + b"\0")
    return digest.hexdigest(), inputs, required_masks


def frozen_workspace_directories():
    """Authorize pointers, never archived contents, to explicitly pinned packages."""
    if not args.source_manifest:
        return set()
    pinned = json.loads(pathlib.Path(args.source_manifest).read_text())['files']
    directories = set()
    for package in workspace_packages():
        name = str(pathlib.PurePosixPath(package) / 'package.json')
        source = root / name
        require(name in pinned and not source.is_symlink() and source.resolve() == source
                and source.is_file() and hashlib.sha256(source.read_bytes()).hexdigest() == pinned[name]['sha256'],
                'Workspace pointer needs a frozen package manifest')
        directories.add(package)
    return directories


def verify_inputs_unchanged():
    current_key, current_inputs, current_masks = snapshot_inputs()
    require(current_key == inputs_key and current_inputs == inputs and current_masks == required_masks,
            "Dependency inputs changed during cache use; discard this result")

cache = root / ".wb-worklog" / "linux-dependency-cache"
if not args.apply and not cache.exists():
    available = shutil.disk_usage(root).free
    print('WB_DEPENDENCY_CACHE_POLICY ' + json.dumps({'mode': 'plan', 'candidates': [],
          'remainingBytes': 0, 'unknownBytes': 0, 'removedBytes': 0, 'budgetSatisfied': True,
          'availableBefore': available, 'availableAfter': available,
          'reserveSatisfied': available >= args.min_free_bytes + args.growth_bytes}))
    sys.exit(0 if available >= args.min_free_bytes + args.growth_bytes else 1)
for directory in (cache.parent, cache):
    require(not directory.is_symlink(), "Cache ancestor is a symlink")
    if args.apply:
        directory.mkdir(mode=0o700, exist_ok=True)
    info = directory.stat()
    require(stat.S_ISDIR(info.st_mode) and info.st_uid == os.getuid() and not info.st_mode & 0o022,
            "Unsafe cache directory ownership or permissions")
cache_fd = os.open(cache, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
lock_fd = os.open('writer.lock', (os.O_CREAT | os.O_RDWR if args.apply else os.O_RDONLY) | os.O_NOFOLLOW,
                  0o600, dir_fd=cache_fd)
lock_info = os.fstat(lock_fd)
require(stat.S_ISREG(lock_info.st_mode) and lock_info.st_uid == os.getuid() and lock_info.st_nlink == 1,
        "Unsafe cache lock")
fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)


def checksum(path):
    h = hashlib.sha256()
    with secure_read(path) as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def secure_read(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    info = os.fstat(descriptor)
    if not (stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and info.st_nlink == 1):
        os.close(descriptor)
        raise RuntimeError("Unsafe cache file")
    return os.fdopen(descriptor, "rb")


def mountinfo_path(value):
    """Decode only the four path escapes emitted by the Linux mount table."""
    escapes = {b'040': b' ', b'011': b'\t', b'012': b'\n', b'134': b'\\'}
    pattern = rb'\\(040|011|012|134)'
    require(not any(c in value for c in (b'\0', b' ', b'\t', b'\n'))
            and b'\\' not in re.sub(pattern, b'', value), 'Invalid Linux mount path escape')
    decoded = os.fsdecode(re.sub(pattern, lambda match: escapes[match[1]], value))
    require(decoded.startswith('/') and not decoded.startswith('//')
            and '..' not in pathlib.PurePosixPath(decoded).parts
            and str(pathlib.PurePosixPath(decoded)) == decoded, 'Noncanonical Linux mount path')
    return decoded


def parse_mountinfo(raw):
    require(isinstance(raw, bytes) and 0 < len(raw) <= 1024**2 and raw.endswith(b'\n') and b'\0' not in raw,
            'Linux mount table byte bound or incomplete record')
    lines = raw[:-1].split(b'\n')
    require(len(lines) <= 4096, 'Linux mount table record bound')
    mounts, identities = {}, set()
    for line in lines:
        require(0 < len(line) <= 16384 and line.count(b' - ') == 1, 'Invalid Linux mount record')
        left, right = (part.split(b' ') for part in line.split(b' - '))
        require(len(left) >= 6 and len(right) == 3 and all(left) and all(right),
                'Invalid Linux mount fields')
        require(all(re.fullmatch(rb'[1-9][0-9]{0,9}', value) is not None
                    and int(value) <= 2**32 - 1 for value in left[:2])
                and re.fullmatch(rb'[0-9]+:[0-9]+', left[2]) is not None,
                'Invalid Linux mount identity')
        identity = int(left[0])
        require(identity not in identities, 'Duplicate Linux mount identity')
        identities.add(identity)
        mountinfo_path(left[3])
        point = mountinfo_path(left[4])
        options, super_options = left[5].split(b','), right[2].split(b',')
        require(all(options) and all(super_options), 'Invalid Linux mount options')
        writable = all(values.count(b'rw') == 1 and b'ro' not in values
                       for values in (options, super_options))
        # Unknown optional fields are intentionally ignored, as the kernel contract requires.
        mounts.setdefault(point, []).append((identity, writable))
    return mounts


def read_mountinfo():
    with open('/proc/self/mountinfo', 'rb') as stream:
        return stream.read(1024**2 + 1)


def masked(name):
    relative = pathlib.PurePosixPath(name)
    if relative.is_absolute() or '..' in relative.parts or str(relative) != name:
        return False
    target = root / name
    if target.is_symlink() or not target.is_dir() or target.resolve() != target:
        return False
    # st_dev-based ismount cannot recognize a bind of a subtree on the same filesystem.
    records = parse_mountinfo(read_mountinfo()).get(str(target), [])
    return len(records) == 1 and records[0][1]


def checked_filter(member):
    name = posixpath.normpath(member.name)
    require(not member.name.startswith("/") and ".." not in pathlib.PurePosixPath(member.name).parts, "Unsafe archive name")
    require(any(name == prefix or name.startswith(prefix + "/") for prefix in selected), "Archive member leaves dependency masks")
    require(member.isfile() or member.isdir() or member.issym() or member.islnk(), "Unsupported archive member")
    if member.issym() or member.islnk():
        require(not member.linkname.startswith("/"), "Absolute archive link")
        named = False
        for component in pathlib.PurePosixPath(member.linkname).parts:
            require(component != '..' or not named, 'Archive link cancels a named path component')
            named = named or component not in ('.', '..')
        link = posixpath.normpath(posixpath.join(
            posixpath.dirname(name) if member.issym() else "", member.linkname))
        require(link != ".." and not link.startswith("../"), "Archive link leaves worktree")
        require(any(link == prefix or link.startswith(prefix + "/") for prefix in selected)
                or (member.issym() and link in workspace_directories),
                "Archive link leaves dependency masks")
    return member


def preflight_archive(path):
    """Validate the complete link graph before publishing or writing any member."""
    entries, symlinks, targets = set(), {}, []
    total = count = 0
    with secure_read(path) as cached, tarfile.open(fileobj=cached, mode='r|gz') as stored:
        for member in stored:
            count += 1
            total += member.size
            require(count <= 500000 and total <= 4 * 1024**3, 'Unpacked cache bound exceeded')
            checked_filter(member)
            name = posixpath.normpath(member.name)
            require(name not in entries, 'Duplicate archive member')
            entries.add(name)
            if member.issym() or member.islnk():
                target = posixpath.normpath(posixpath.join(
                    posixpath.dirname(name) if member.issym() else '', member.linkname))
                targets.append((name, target, member.islnk()))
                if member.issym():
                    symlinks[name] = target
    pointers = {name for name, target in symlinks.items()
                if not any(target == prefix or target.startswith(prefix + '/') for prefix in selected)}

    def prefixes(name):
        parts = pathlib.PurePosixPath(name).parts
        return ['/'.join(parts[:i]) for i in range(1, len(parts) + 1)]

    for name in entries:
        require(not any(parent in symlinks for parent in prefixes(name)[:-1]),
                'Archive member traverses a symlink')
    for name, target, hardlink in targets:
        if name in pointers:
            continue  # Only this exact symlink may point to the frozen package root.
        seen = set()
        while True:
            crossed = next((p for p in prefixes(target) if p in symlinks), None)
            if crossed is None:
                break
            require(not hardlink and crossed not in pointers and crossed not in seen,
                    'Archive link traverses a workspace pointer, hardlink alias, or cycle')
            seen.add(crossed)
            suffix = target[len(crossed):].lstrip('/')
            target = posixpath.normpath(posixpath.join(symlinks[crossed], suffix))
            require(any(target == prefix or target.startswith(prefix + '/') for prefix in selected),
                    'Archive link route leaves dependency masks')
    return count, total


def destination_for(name):
    """Walk only ordinary directories below a verified dependency tmpfs."""
    name = posixpath.normpath(name)
    prefix = max((p for p in selected if name == p or name.startswith(p + "/")), key=len)
    base = root / prefix
    current = base
    relative = pathlib.PurePosixPath(name).relative_to(prefix)
    for component in relative.parts[:-1]:
        current = current / component
        require(not current.is_symlink(), "Refuse extraction through a symlink")
        current.mkdir(exist_ok=True)
        require(current.is_dir(), "Extraction ancestor is not a directory")
    return root / name


def cache_policy(apply=False, active=None, growth=0):
    """Evict only completed immutable archive pairs, holding the producer's lock."""
    opened, current = os.fstat(cache_fd), cache.lstat()
    held, named = os.fstat(lock_fd), (cache / 'writer.lock').lstat()
    require((opened.st_dev, opened.st_ino) == (current.st_dev, current.st_ino)
            and (held.st_dev, held.st_ino) == (named.st_dev, named.st_ino), 'Cache or stable lock identity changed')
    available = shutil.disk_usage(cache).free
    entries = {}
    for path in cache.iterdir():
        require(len(entries) < 1000, 'Cache inventory bound exceeded')
        info = path.lstat()
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid()
                and info.st_nlink == 1 and not info.st_mode & 0o022, 'Unsafe cache entry')
        if path.name != 'writer.lock':
            entries[path.name] = info
    pairs = []
    for name, info in entries.items():
        match = re.fullmatch(r'([0-9a-f]{64})\.tar\.gz', name)
        if not match or match[1] + '.json' not in entries:
            continue
        sidecar = cache / (match[1] + '.json')
        require(entries[sidecar.name].st_size <= 65536, 'Oversized archive manifest')
        with secure_read(sidecar) as stream:
            meta = json.load(stream)
        if meta.get('format') != 'wb-dependency-cache/1' or meta.get('complete') is not True:
            continue
        require(meta.get('identity', {}).get('schema') == 4
                and re.fullmatch(r'[0-9a-f]{64}', meta.get('sha256', '')) is not None,
                'Invalid completed archive identity')
        require(hashlib.sha256(json.dumps(meta['identity'], sort_keys=True).encode()).hexdigest() == match[1],
                'Completed archive key differs from identity')
        used = meta.get('lastUsed')
        require(type(used) in (int, float) and 0 < used <= time.time() + 60,
                'Invalid completed archive age')
        pairs.append((used, name, sidecar.name))
    remaining = sum(info.st_size for info in entries.values())
    remaining_count = len(pairs)
    report = {'mode': 'apply' if apply else 'plan', 'availableBefore': available,
              'candidates': [], 'removedBytes': 0,
              'unknownBytes': remaining - sum(entries[a].st_size + entries[m].st_size for _, a, m in pairs)}
    for used, archive_name, manifest_name in sorted(pairs):
        stale = time.time() - used > args.max_age_seconds
        pressure = remaining > args.max_bytes or remaining_count > 3 or available < args.min_free_bytes + growth
        delete = archive_name != active and (stale or pressure)
        size = entries[archive_name].st_size + entries[manifest_name].st_size
        report['candidates'].append({'name': archive_name, 'bytes': size,
            'action': 'delete' if delete else 'keep',
            'reason': 'active' if archive_name == active else 'age' if stale else 'budget-or-reserve' if pressure else 'within-policy'})
        if not delete:
            continue
        if apply:
            for name in (archive_name, manifest_name):
                old = entries[name]
                now = os.stat(name, dir_fd=cache_fd, follow_symlinks=False)
                require((now.st_dev, now.st_ino, now.st_size, now.st_mtime_ns)
                        == (old.st_dev, old.st_ino, old.st_size, old.st_mtime_ns), 'Cache entry changed before deletion')
            os.unlink(archive_name, dir_fd=cache_fd)
            os.unlink(manifest_name, dir_fd=cache_fd)
            os.fsync(cache_fd)
            report['removedBytes'] += size
            available = shutil.disk_usage(cache).free
        else:
            available += sum(entries[n].st_blocks * 512 for n in (archive_name, manifest_name))
        remaining -= size
        remaining_count -= 1
    report.update(remainingBytes=remaining, availableAfter=available,
                  budgetSatisfied=remaining <= args.max_bytes and remaining_count <= 3,
                  reserveSatisfied=available >= args.min_free_bytes + growth)
    print('WB_DEPENDENCY_CACHE_POLICY ' + json.dumps(report), flush=True)
    if apply:
        require(report['budgetSatisfied'] and report['reserveSatisfied'],
                'Cache budget/reserve unreachable; preserve active and unknown files')
    return report


if not args.apply:
    plan = cache_policy(growth=args.growth_bytes)
    sys.exit(1 if not plan['reserveSatisfied'] or not plan['budgetSatisfied'] or any(c['action'] == 'delete' for c in plan['candidates']) else 0)
inputs_key, inputs, required_masks = snapshot_inputs()
workspace_directories = frozen_workspace_directories()
require(all(masked(n) for n in required_masks),
        'Refuse dependency work before every workspace dependency target is masked')
require(subprocess.check_output(['pnpm', '--version'], text=True, timeout=10).strip() == version,
        'Installed pnpm differs from packageManager')
identity = {'schema': 4, 'inputs': inputs_key, 'image': image, 'pnpm': manager,
            'node': subprocess.check_output(['node', '--version'], text=True, timeout=10).strip(),
            'arch': os.uname().machine}
key = hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()
archive = cache / (key + '.tar.gz')
manifest = cache / (key + '.json')
require(archive.exists() == manifest.exists(), 'Incomplete active cache pair is preserved')
cache_policy(True, archive.name, args.growth_bytes)
allowed = {'node_modules'}
for name in inputs:
    if name.endswith('/package.json'):
        allowed.add(str(pathlib.PurePosixPath(name).parent / 'node_modules'))


hit = False
reason = "missing cache"
if manifest.is_file() and archive.is_file():
    with secure_read(manifest) as metadata:
        meta = json.load(metadata)
    require(meta.get('format') == 'wb-dependency-cache/1' and meta.get('complete') is True,
            'Unknown active cache manifest is preserved')
    selected = meta["roots"]
    require(meta["identity"] == identity, "Cache identity mismatch")
    require(selected and set(selected) <= allowed and all(masked(n) for n in selected), "Missing Linux dependency mask")
    require(archive.stat().st_size <= 2 * 1024**3, "Cache archive size bound exceeded")
    if checksum(archive) == meta["sha256"]:
        require(all(not any((root / n).iterdir()) for n in selected), "Restore only into empty Linux masks")
        preflight_archive(archive)
        total = count = 0
        links = []
        with secure_read(archive) as cached, tarfile.open(fileobj=cached, mode="r|gz") as stored:
            for member in stored:
                count += 1
                total += member.size
                require(count <= 500000 and total <= 4 * 1024**3, "Unpacked cache bound exceeded")
                checked_filter(member)
                if member.issym() or member.islnk():
                    links.append(member)
                    continue
                target = destination_for(member.name)
                if member.isdir():
                    require(not target.is_symlink(), "Directory target is a symlink")
                    target.mkdir(exist_ok=True)
                else:
                    with target.open("xb") as output:
                        source = stored.extractfile(member)
                        require(source is not None, "Archive file data missing")
                        shutil.copyfileobj(source, output, 1024 * 1024)
                    target.chmod(member.mode & 0o777)
        # No archive symlink exists while regular files are being written.
        # Hard links can only name an already materialized ordinary file.
        for member in (m for m in links if m.islnk()):
            target = destination_for(member.name)
            source = destination_for(member.linkname)
            require(source.is_file() and not source.is_symlink(), "Hard link target is not an ordinary file")
            os.link(source, target, follow_symlinks=False)
        for member in (m for m in links if m.issym()):
            target = destination_for(member.name)
            target.symlink_to(member.linkname)
        require(all((root / n).is_file() for n in (
            "node_modules/.bin/nx", "node_modules/.bin/playwright")), "Cached executable shim missing")
        verify_inputs_unchanged()
        hit = True
        print("WB_LINUX_DEPENDENCIES_CACHE_HIT " + json.dumps({
            "restoreSeconds": round(time.monotonic() - started, 3),
            "archiveBytes": archive.stat().st_size, "entries": count,
            "installSkipped": True, "hostDependencyMasksPreserved": True}), flush=True)
    else:
        reason = "checksum mismatch"
if not hit:
    print("WB_LINUX_DEPENDENCIES_CACHE_MISS " + reason, flush=True)
    require(all(masked(n) for n in required_masks), "Refuse install before every workspace dependency target is masked")
    subprocess.run(["pnpm", "install", "--frozen-lockfile",
                    "--store-dir", args.store_dir, "--child-concurrency=1",
                    "--network-concurrency=1"], check=True, timeout=420, pass_fds=(lock_fd,))
    verify_inputs_unchanged()
    installed = time.monotonic()
    selected = sorted(n for n in allowed if (root / n).is_dir())
    require(selected and all(masked(n) for n in selected), "Cache only Linux tmpfs dependencies")
    descriptor, temporary_name = tempfile.mkstemp(prefix=".wb-cache-", suffix=".tmp", dir=cache)
    temporary = pathlib.Path(temporary_name)
    total = count = 0

    def pack_filter(member):
        global total, count
        count += 1
        total += member.size
        require(count <= 500000 and total <= 4 * 1024**3, "Cache packing bound exceeded")
        return checked_filter(member)

    try:
        with os.fdopen(descriptor, "wb") as output, tarfile.open(
                fileobj=BoundedArchive(output, min(args.max_bytes, 2 * 1024**3)),
                mode="w:gz", compresslevel=1, dereference=False) as stored:
            for name in selected:
                stored.add(root / name, arcname=name, filter=pack_filter)
        require(temporary.stat().st_size <= 2 * 1024**3, "Cache archive size bound exceeded")
        temporary.chmod(0o600)
        with temporary.open('rb') as completed:
            os.fsync(completed.fileno())
        preflight_archive(temporary)
        meta = {"identity": identity, "sha256": checksum(temporary), "roots": selected,
                "entries": count, "unpackedBytes": total, 'format': 'wb-dependency-cache/1',
                'complete': True, 'lastUsed': time.time()}
        verify_inputs_unchanged()
        temporary.replace(archive)
        metadata_fd, staged_name = tempfile.mkstemp(prefix=".wb-cache-", suffix=".tmp", dir=cache)
        staged = pathlib.Path(staged_name)
        with os.fdopen(metadata_fd, "w") as output:
            output.write(json.dumps(meta, indent=2) + "\n")
            output.flush()
            os.fsync(output.fileno())
        staged.replace(manifest)
        os.fsync(cache_fd)
    finally:
        temporary.unlink(missing_ok=True)
    print("WB_LINUX_DEPENDENCIES_CACHE_FILLED " + json.dumps({
        "installSeconds": round(installed - started, 3),
        "archiveSeconds": round(time.monotonic() - installed, 3),
        "archiveBytes": archive.stat().st_size, "entries": count,
        "hostDependencyMasksPreserved": True}), flush=True)

if hit:
    meta['lastUsed'] = time.time()
    descriptor, name = tempfile.mkstemp(prefix='.wb-cache-', suffix='.tmp', dir=cache)
    with os.fdopen(descriptor, 'w') as stream:
        json.dump(meta, stream)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(name, manifest)
    os.fsync(cache_fd)
cache_policy(True, archive.name)
verify_inputs_unchanged()
