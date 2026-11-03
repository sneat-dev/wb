"""Real temporary filesystem/lease tests; package tools and cache jobs never run."""
import ast
import contextlib
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import tarfile
import time
import types
import unittest
from unittest.mock import patch

ASSET = Path(__file__).with_name('cache.py')


def functions():
    tree = ast.parse(ASSET.read_text())
    selected = [n for n in tree.body if isinstance(n, (ast.Import, ast.ImportFrom, ast.FunctionDef, ast.ClassDef))]
    namespace = {}
    exec(compile(ast.Module(body=selected, type_ignores=[]), str(ASSET), 'exec'), namespace)
    return namespace


def mount_record(point, identity=21, parent=14, options='rw,relatime', super_options='rw',
                 mount_root='/qdv/dependencies', optional=()):
    def encoded(path):
        return os.fsencode(path).replace(b'\\', b'\\134').replace(b' ', b'\\040').replace(b'\t', b'\\011').replace(b'\n', b'\\012')
    fields = [str(identity).encode(), str(parent).encode(), b'0:47', encoded(mount_root),
              encoded(point), options.encode(), *optional]
    return b' '.join(fields) + b' - ext4 /dev/fictional ' + super_options.encode() + b'\n'


class ArchivePolicyTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.cache = self.root / '.wb-worklog/linux-dependency-cache'
        self.cache.mkdir(parents=True, mode=0o700)
        self.cache_fd = os.open(self.cache, os.O_RDONLY | os.O_DIRECTORY)
        self.lock_fd = os.open(self.cache / 'writer.lock', os.O_CREAT | os.O_RDWR, 0o600)
        fcntl.flock(self.lock_fd, fcntl.LOCK_EX)
        self.addCleanup(os.close, self.cache_fd)
        self.addCleanup(lambda: os.close(self.lock_fd) if self.lock_fd >= 0 else None)
        self.ns = functions()
        self.ns.update(root=self.root, cache=self.cache, cache_fd=self.cache_fd, lock_fd=self.lock_fd,
                       args=types.SimpleNamespace(max_bytes=100000, max_age_seconds=86400,
                                                 min_free_bytes=1, source_manifest=None))

    def pair(self, digit, age=0):
        identity = {'schema': 4, 'fixture': digit}
        key = hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()
        archive = self.cache / (key + '.tar.gz')
        archive.write_bytes(b'fictional immutable archive')
        meta = {'format': 'wb-dependency-cache/1', 'identity': identity,
                'complete': True, 'lastUsed': time.time() - age,
                'sha256': hashlib.sha256(archive.read_bytes()).hexdigest()}
        sidecar = self.cache / (key + '.json')
        sidecar.write_text(json.dumps(meta))
        return archive, sidecar

    def snapshot(self):
        return {p.name: (p.read_bytes(), p.stat().st_mtime_ns) for p in self.cache.iterdir()}

    def policy(self, apply=False, active=None, growth=0):
        with contextlib.redirect_stdout(io.StringIO()):
            return self.ns['cache_policy'](apply, active, growth)

    def test_plan_preserves_all_bytes_and_mtimes_and_protects_active_archive(self):
        old, old_meta = self.pair('1', 100)
        active, active_meta = self.pair('2')
        self.ns['args'].max_bytes = active.stat().st_size + active_meta.stat().st_size
        before = self.snapshot()
        report = self.policy(active=active.name)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual([c['name'] for c in report['candidates'] if c['action'] == 'delete'], [old.name])
        self.assertTrue(report['budgetSatisfied'])

    def test_compressed_writer_refuses_growth_before_crossing_the_byte_bound(self):
        stream = io.BytesIO()
        limited = self.ns['BoundedArchive'](stream, 4)
        self.assertEqual(limited.write(b'1234'), 4)
        self.assertEqual(limited.tell(), 4)
        with self.assertRaisesRegex(RuntimeError, 'Compressed cache bound'):
            limited.write(b'5')
        self.assertEqual(stream.getvalue(), b'1234')

    def test_apply_removes_only_old_completed_pairs_and_enforces_aggregate_budget(self):
        old, old_meta = self.pair('1', 100)
        active, active_meta = self.pair('2')
        self.ns['args'].max_bytes = active.stat().st_size + active_meta.stat().st_size
        report = self.policy(True, active.name)
        self.assertFalse(old.exists())
        self.assertFalse(old_meta.exists())
        self.assertTrue(active.exists() and active_meta.exists())
        self.assertGreater(report['removedBytes'], 0)
        self.assertTrue(report['budgetSatisfied'])

    def test_four_identities_retain_only_three_most_recent_generations(self):
        oldest, _ = self.pair('1', 300)
        self.pair('2', 200)
        self.pair('3', 100)
        active, _ = self.pair('4')
        self.policy(True, active.name)
        self.assertFalse(oldest.exists())
        self.assertEqual(len(list(self.cache.glob('*.tar.gz'))), 3)

    def test_age_and_free_space_pressure_each_select_unused_generations(self):
        old, old_meta = self.pair('1', 172800)
        self.policy(True)
        self.assertFalse(old.exists() or old_meta.exists())
        self.pair('2')
        self.ns['args'].min_free_bytes = 9900
        def available(_):
            return types.SimpleNamespace(free=10000 - sum(p.stat().st_size for p in self.cache.iterdir()))
        with patch.object(self.ns['shutil'], 'disk_usage', available):
            self.assertTrue(self.policy(True)['reserveSatisfied'])

    def test_unknown_staged_files_and_active_archive_make_unreachable_budget_fail_closed(self):
        active, _ = self.pair('1', 172800)
        orphan = self.cache / '.wb-cache-interrupted.tmp'
        orphan.write_bytes(b'preserve unknown partial producer evidence')
        self.ns['args'].max_bytes = 1
        before = self.snapshot()
        with self.assertRaisesRegex(RuntimeError, 'unreachable'):
            self.policy(True, active.name)
        self.assertEqual(self.snapshot(), before)

    def test_symlink_and_corrupt_manifest_abort_before_any_eviction(self):
        old, meta = self.pair('1', 172800)
        outside = self.root / 'fictional-source'
        outside.write_bytes(b'keep source')
        link = self.cache / 'unexpected-link'
        link.symlink_to(outside)
        with self.assertRaisesRegex(RuntimeError, 'Unsafe cache entry'):
            self.policy(True)
        self.assertTrue(old.exists())
        self.assertEqual(outside.read_bytes(), b'keep source')
        link.unlink()
        meta.write_text('{')
        with self.assertRaises(json.JSONDecodeError):
            self.policy(True)
        self.assertTrue(old.exists())

    def test_replaced_root_or_lock_inode_is_not_authority_to_delete(self):
        old, _ = self.pair('1', 172800)
        (self.cache / 'writer.lock').unlink()
        (self.cache / 'writer.lock').write_bytes(b'')
        with self.assertRaisesRegex(RuntimeError, 'identity changed'):
            self.policy(True)
        self.assertTrue(old.exists())

    def test_replaced_cache_root_preserves_both_original_and_replacement(self):
        old, _ = self.pair('1', 172800)
        retained = self.cache.with_name('retained-original')
        self.cache.rename(retained)
        self.cache.mkdir(mode=0o700)
        (self.cache / 'writer.lock').write_bytes(b'')
        with self.assertRaisesRegex(RuntimeError, 'identity changed'):
            self.policy(True)
        self.assertTrue((retained / old.name).exists())
        self.assertEqual([p.name for p in self.cache.iterdir()], ['writer.lock'])

    def test_archive_links_cannot_reach_source_outside_dependency_masks(self):
        self.ns['selected'] = ['node_modules', 'landings/incidentius/node_modules']
        unsafe = tarfile.TarInfo('node_modules/escape')
        unsafe.type = tarfile.SYMTYPE
        unsafe.linkname = '../source.py'
        with self.assertRaisesRegex(RuntimeError, 'leaves dependency masks'):
            self.ns['checked_filter'](unsafe)
        unsafe.name = '../outside'
        with self.assertRaisesRegex(RuntimeError, 'Unsafe archive name'):
            self.ns['checked_filter'](unsafe)

    def test_failed_second_unlink_retains_manifest_and_reports_failure(self):
        archive, meta = self.pair('1', 172800)
        real = os.unlink
        def remove(name, *a, **kw):
            if name == meta.name:
                raise PermissionError('fixture deletion refusal')
            return real(name, *a, **kw)
        with patch.object(self.ns['os'], 'unlink', remove):
            with self.assertRaises(PermissionError):
                self.policy(True)
        self.assertFalse(archive.exists())
        self.assertTrue(meta.exists())
        self.assertGreater(self.policy()['unknownBytes'], 0)

    def test_frozen_manifest_inputs_need_no_git_and_reject_source_drift(self):
        package = {'packageManager': 'pnpm@12.16.1'}
        inputs = {'package.json': json.dumps(package), 'pnpm-workspace.yaml': "packages:\n  - '.'\n  - 'landings/incidentius'\n",
                  'pnpm-lock.yaml': 'fictional lock', 'landings/incidentius/package.json': '{}'}
        for name, data in inputs.items():
            p = self.root / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(data)
        manifest = self.root / 'frozen.json'
        manifest.write_text(json.dumps({'files': {n: {'sha256': hashlib.sha256(v.encode()).hexdigest()} for n, v in inputs.items()}}))
        self.ns['args'].source_manifest = str(manifest)
        with patch.object(self.ns['subprocess'], 'check_output', side_effect=AssertionError('git must not run')):
            _, _, masks = self.ns['snapshot_inputs']()
            self.assertEqual(masks, {'node_modules', 'landings/incidentius/node_modules'})
            (self.root / 'pnpm-lock.yaml').write_text('changed')
            with self.assertRaisesRegex(RuntimeError, 'frozen source manifest'):
                self.ns['snapshot_inputs']()

    def child_only_inputs(self, package):
        inputs = {'package.json': json.dumps(package),
                  'pnpm-workspace.yaml': "packages:\n  - 'landings/incidentius'\n",
                  'pnpm-lock.yaml': 'fictional lock', 'landings/incidentius/package.json': '{}',
                  'local.tgz': 'fictional local package'}
        for name, data in inputs.items():
            p = self.root / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(data)
        manifest = self.root / 'frozen.json'
        manifest.write_text(json.dumps({'files': {n: {'sha256': hashlib.sha256(v.encode()).hexdigest()} for n, v in inputs.items()}}))
        self.ns['args'].source_manifest = str(manifest)

    def admission(self):
        body = ast.parse(ASSET.read_text()).body
        start = next(i for i, n in enumerate(body) if isinstance(n, ast.Assign)
                     and isinstance(n.targets[0], ast.Tuple)
                     and isinstance(n.targets[0].elts[0], ast.Name) and n.targets[0].elts[0].id == 'inputs_key')
        end = next(i for i in range(start, len(body)) if isinstance(body[i], ast.Assign)
                   and isinstance(body[i].targets[0], ast.Name) and body[i].targets[0].id == 'identity')
        return compile(ast.Module(body=body[start:end], type_ignores=[]), str(ASSET), 'exec')

    def test_same_filesystem_mount_records_admit_every_mask_before_the_version_probe(self):
        self.child_only_inputs({'packageManager': 'pnpm@12.16.1'})
        self.ns['version'] = '12.16.1'
        targets = [self.root / 'node_modules', self.root / 'landings/incidentius/node_modules']
        for target in targets:
            target.mkdir(parents=True, exist_ok=True)
            self.assertEqual(target.stat().st_dev, self.root.stat().st_dev)
            self.assertFalse(os.path.ismount(target))
        raw = mount_record(self.root, identity=14, parent=1, mount_root='/')
        raw += mount_record(targets[0], optional=(b'shared:7', b'future:ignored'))
        raw += mount_record(targets[1], identity=22, mount_root='/qdv/incidentius')
        self.ns['read_mountinfo'] = lambda: raw
        self.assertTrue(self.ns['masked']('node_modules'))
        self.assertTrue(self.ns['masked']('landings/incidentius/node_modules'))
        with patch.object(self.ns['subprocess'], 'check_output', return_value='12.16.1') as called:
            exec(self.admission(), self.ns)
            called.assert_called_once_with(['pnpm', '--version'], text=True, timeout=10)
        self.assertEqual(self.ns['required_masks'], {'node_modules', 'landings/incidentius/node_modules'})

    def test_mount_records_decode_kernel_paths_without_double_unescaping(self):
        project = self.root / 'project space\ttab\nline\\040'
        target = project / 'node_modules'
        target.mkdir(parents=True)
        self.ns['root'] = project
        raw = mount_record(project, identity=14, parent=1, mount_root='/') + mount_record(target)
        self.ns['read_mountinfo'] = lambda: raw
        self.assertTrue(self.ns['masked']('node_modules'))
        self.assertIn(str(target), self.ns['parse_mountinfo'](raw))

    def test_masks_need_one_exact_writable_mount_not_a_parent_or_descendant(self):
        target = self.root / 'node_modules'
        target.mkdir()
        cases = {
            'parent only': mount_record(self.root),
            'descendant only': mount_record(target / 'nested'),
            'duplicate point': mount_record(target) + mount_record(target, identity=22),
            'read-only mount': mount_record(target, options='ro,relatime'),
            'read-only superblock': mount_record(target, super_options='ro'),
            'contradictory mount flags': mount_record(target, options='rw,ro'),
            'contradictory superblock flags': mount_record(target, super_options='rw,ro'),
            'repeated writable flag': mount_record(target, options='rw,rw'),
            'missing writable flag': mount_record(target, options='relatime'),
        }
        for label, raw in cases.items():
            with self.subTest(label=label):
                self.ns['read_mountinfo'] = lambda: raw
                self.assertFalse(self.ns['masked']('node_modules'))

    def test_mount_records_reject_malformed_identities_paths_and_bounds(self):
        target = self.root / 'node_modules'
        target.mkdir()
        valid = mount_record(target)
        cases = {
            'empty': b'',
            'incomplete': valid[:-1],
            'zero id': mount_record(target, identity=0),
            'negative id': mount_record(target, identity=-1),
            'oversized id': mount_record(target, identity=2**32),
            'zero parent': mount_record(target, parent=0),
            'duplicate id': valid + mount_record(self.root, identity=21),
            'wrong escape': valid.replace(b'node_modules', b'node\\057modules'),
            'literal backslash': valid.replace(b'node_modules', b'node\\modules'),
            'nul path': valid.replace(b'node_modules', b'node\0modules'),
            'path alias': mount_record(str(target) + '/../node_modules'),
            'extra separator': valid.replace(b' - ', b' - extra - '),
            'missing field': valid.replace(b'ext4 /dev/fictional rw', b'ext4 rw'),
            'byte bound': b'x' * (1024**2) + b'\n',
            'record bound': valid * 4097,
            'line bound': valid[:-1] + b'x' * 16384 + b'\n',
        }
        for label, raw in cases.items():
            with self.subTest(label=label):
                self.ns['read_mountinfo'] = lambda: raw
                with self.assertRaises(RuntimeError):
                    self.ns['masked']('node_modules')

    def test_mask_mount_records_do_not_authorize_files_or_symlink_aliases(self):
        target = self.root / 'node_modules'
        actual = self.root / 'actual'
        actual.mkdir()
        target.symlink_to(actual, target_is_directory=True)
        self.ns['read_mountinfo'] = lambda: mount_record(target)
        self.assertFalse(self.ns['masked']('node_modules'))
        target.unlink()
        target.write_text('not a directory')
        self.assertFalse(self.ns['masked']('node_modules'))
        target.unlink()
        (actual / 'incidentius/node_modules').mkdir(parents=True)
        (self.root / 'landings').symlink_to(actual, target_is_directory=True)
        alias = self.root / 'landings/incidentius/node_modules'
        self.ns['read_mountinfo'] = lambda: mount_record(alias)
        self.assertFalse(self.ns['masked']('landings/incidentius/node_modules'))
        self.assertFalse(self.ns['masked']('../actual'))
        self.assertFalse(self.ns['masked'](str(actual)))

    def test_mountinfo_reader_is_bounded_and_unavailable_authority_refuses(self):
        with patch('builtins.open', return_value=io.BytesIO(b'x' * (1024**2 + 50))) as opened:
            raw = self.ns['read_mountinfo']()
            opened.assert_called_once_with('/proc/self/mountinfo', 'rb')
        self.assertEqual(len(raw), 1024**2 + 1)
        with self.assertRaisesRegex(RuntimeError, 'byte bound'):
            self.ns['parse_mountinfo'](raw)
        (self.root / 'node_modules').mkdir()
        self.ns['read_mountinfo'] = lambda: (_ for _ in ()).throw(PermissionError('mount table unavailable'))
        with self.assertRaises(PermissionError):
            self.ns['masked']('node_modules')

    def test_child_only_workspace_refuses_unmounted_root_before_any_package_command(self):
        self.child_only_inputs({'packageManager': 'pnpm@12.16.1'})
        canary = self.root / 'source-canary'
        canary.write_bytes(b'unchanged source')
        self.ns['version'] = '12.16.1'
        # Execute the actual apply admission statements up to identity/tool probing.
        child = self.root / 'landings/incidentius/node_modules'
        child.mkdir(parents=True)
        self.ns['read_mountinfo'] = lambda: mount_record(child)
        with patch.object(self.ns['subprocess'], 'check_output', side_effect=AssertionError('package command ran')) as called:
            with self.assertRaisesRegex(RuntimeError, 'every workspace dependency target is masked'):
                exec(self.admission(), self.ns)
            called.assert_not_called()
        self.assertEqual(canary.read_bytes(), b'unchanged source')
        self.assertFalse((self.root / 'node_modules').exists())

    def test_child_only_workspace_still_checks_root_lifecycle_and_local_dependencies(self):
        self.child_only_inputs({'packageManager': 'pnpm@12.16.1', 'dependencies': {'local': 'file:local.tgz'}})
        _, inputs, masks = self.ns['snapshot_inputs']()
        self.assertIn('package.json', inputs)
        self.assertIn('local.tgz', inputs)
        self.assertIn('node_modules', masks)
        (self.root / 'local.tgz').unlink()
        with self.assertRaisesRegex(RuntimeError, 'Local directory dependencies'):
            self.ns['snapshot_inputs']()
        self.child_only_inputs({'packageManager': 'pnpm@12.16.1', 'scripts': {'postinstall': 'unapproved lifecycle'}})
        with self.assertRaisesRegex(RuntimeError, 'Workspace lifecycle inputs'):
            self.ns['snapshot_inputs']()

    def test_lease_is_inherited_and_plan_cannot_reclaim_until_last_consumer_exits(self):
        (self.root / 'package.json').write_text('{"packageManager":"pnpm@12.16.1"}')
        child = subprocess.Popen([sys.executable, '-c', 'import sys; print("ready",flush=True); sys.stdin.buffer.read()'],
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE, pass_fds=(self.lock_fd,))
        try:
            self.assertEqual(child.stdout.readline(), b'ready\n')
            os.close(self.lock_fd)
            self.lock_fd = -1
            env = {'PATH': '/usr/bin:/bin'}
            blocked = subprocess.run([sys.executable, str(ASSET), '--min-free-bytes', '1', '--growth-bytes', '1'], cwd=self.root, env=env, capture_output=True, timeout=5)
            self.assertNotEqual(blocked.returncode, 0)
            self.assertIn(b'BlockingIOError', blocked.stderr)
        finally:
            child.stdin.close()
            child.wait(timeout=5)
            child.stdout.close()
        released = subprocess.run([sys.executable, str(ASSET), '--min-free-bytes', '1', '--growth-bytes', '1'], cwd=self.root, env=env, capture_output=True, timeout=5)
        self.assertEqual(released.returncode, 0, released.stderr)
        self.assertIn(b'WB_DEPENDENCY_CACHE_POLICY', released.stdout)


if __name__ == '__main__':
    unittest.main()
