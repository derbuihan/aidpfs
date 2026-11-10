#!/usr/bin/env python3
"""Isolated AIDP FILE/FOLDER rename and delete probe.

Creates one random root under /Workspace/Shared and only synthetic children.
It never deletes an entry it did not create, never deletes a non-empty folder
during cleanup, and stops cleanup when any outcome is unknown. A mode-0600
journal records the root until cleanup is confirmed.

Run with an environment that provides the OCI Python SDK and requests, e.g.
  uv run --no-project --with oci --with requests python -I scripts/aidp-mutation-probe.py --journal /tmp/x.json
"""
import argparse
import base64
import concurrent.futures
import json
import os
import pathlib
import sys
import time
import uuid
from urllib.parse import quote

import oci
import requests

# Match Go url.PathEscape, which aidpfs uses for contents paths.
GO_PATH_SAFE = "$&+,;=:@-_.~"


class Unknown(Exception):
    pass


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--profile', default='OCI_TOKYO')
    p.add_argument('--endpoint', default='https://aidp.ap-osaka-1.oci.oraclecloud.com')
    p.add_argument('--platform-name', default='aidp-sandbox')
    p.add_argument('--workspace-name', default='sandbox')
    p.add_argument('--journal', required=True)
    p.add_argument('--race-rounds', type=int, default=3)
    p.add_argument('--c-from', type=int, default=0)
    p.add_argument('--mount-binary', help='aidpfs executable for case M (real FUSE mount of the probe root)')
    p.add_argument('--region', default='ap-osaka-1')
    p.add_argument('--cases', default='ABCDEFG', help='subset of case letters to run')
    a = p.parse_args()

    c = oci.config.from_file(profile_name=a.profile)
    signer = oci.signer.Signer(tenancy=c['tenancy'], user=c['user'], fingerprint=c['fingerprint'],
                               private_key_file_location=c['key_file'], pass_phrase=c.get('pass_phrase'))
    session = requests.Session()
    session.auth = signer

    # The platform lives in a child compartment, so search the whole subtree.
    idc = oci.identity.IdentityClient(c)
    comps = [c['tenancy']] + [x.id for x in oci.pagination.list_call_get_all_results(
        idc.list_compartments, c['tenancy'], compartment_id_in_subtree=True, lifecycle_state='ACTIVE').data]
    plats = []
    for comp in comps:
        r = session.get(a.endpoint + '/20240831/aiDataPlatforms', params={'compartmentId': comp}, timeout=20)
        if r.ok:
            plats += [x for x in r.json().get('items', []) if x.get('displayName') == a.platform_name and x.get('lifecycleState') == 'ACTIVE']
    if len(plats) != 1:
        sys.exit('platform not uniquely found')
    pbase = a.endpoint + '/20260430/aiDataPlatforms/' + quote(plats[0]['id'], safe='')
    ws = session.get(pbase + '/workspaces', timeout=20)
    ws.raise_for_status()
    match = [x for x in ws.json().get('items', []) if a.workspace_name in (x.get('displayName'), x.get('name'))]
    if len(match) != 1:
        sys.exit('workspace not uniquely found')
    wkey = match[0].get('key') or match[0].get('workspaceKey') or match[0].get('id')
    base = pbase + '/workspaces/' + quote(wkey, safe='')

    root = '/Workspace/Shared/aidpfs-mutation-probe-' + uuid.uuid4().hex
    fd = os.open(a.journal, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as f:
        json.dump({'root': root, 'profile': a.profile, 'platform': a.platform_name, 'workspace': a.workspace_name}, f)
        f.flush()
        os.fsync(f.fileno())

    owned_files = set()
    owned_folders = []  # creation order; deleted in reverse
    unknown = []
    results = []

    def log(*args):
        print(*args, flush=True)

    def inside(path):
        if not (path == root or path.startswith(root + '/')):
            raise ValueError('path outside probe root')
        return path

    def contents_url(path):
        return base + '/notebook/api/contents/' + quote(inside(path).lstrip('/'), safe=GO_PATH_SAFE)

    def req(label, method, url, timeout=30, **kw):
        try:
            r = session.request(method, url, timeout=timeout, allow_redirects=False, **kw)
        except requests.exceptions.RequestException as e:
            log(f'  {label}: {method} EXC {type(e).__name__}')
            raise Unknown(label)
        msg = ''
        if r.status_code >= 400:
            try:
                msg = ' code=%s message=%s' % (r.json().get('code'), r.json().get('message'))
            except ValueError:
                pass
        log(f'  {label}: {method} {r.status_code} rid={r.headers.get("opc-request-id")}{msg}')
        return r

    def listing(path):
        items, page, seen = [], None, set()
        while True:
            q = {'path': inside(path) if path != '/Workspace/Shared' else path, 'limit': 1000}
            if page:
                q['page'] = page
            r = session.get(base + '/objects', params=q, timeout=30)
            r.raise_for_status()
            items += r.json()['items']
            page = r.headers.get('opc-next-page')
            if not page:
                return {x['path']: x.get('type') for x in items}
            if page in seen:
                raise RuntimeError('repeated page token')
            seen.add(page)

    def create(path, kind, data=b''):
        h = {'path': inside(path).encode('utf-8'), 'type': kind, 'is-overwrite': 'false',
             'Content-Type': 'application/octet-stream'}
        r = req('create ' + kind, 'POST', base + '/objects', headers=h, data=data)
        if r.status_code not in (200, 201):
            raise RuntimeError(f'create failed {r.status_code}')
        (owned_folders.append(path) if kind == 'FOLDER' else owned_files.add(path))

    def read(path):
        r = session.get(contents_url(path), params={'content': 1}, timeout=30)
        if r.status_code != 200:
            return r.status_code, None
        m = r.json()
        if m.get('type') == 'directory':
            return 200, 'DIR'
        raw = m.get('content') or ''
        if isinstance(raw, dict):
            return 200, ('NB:' + str(raw.get('metadata', {}).get('aidpfs_marker'))).encode()
        return 200, base64.b64decode(raw) if m.get('format') == 'base64' else raw.encode()

    def rename(label, src, dst, timeout=30):
        return req(label, 'PATCH', contents_url(src), timeout=timeout,
                   headers={'Content-Type': 'application/json'},
                   data=json.dumps({'path': inside(dst).lstrip('/')}))

    def moved(src, dst):
        owned_files.discard(src)
        owned_files.add(dst)

    def tree(path):
        out = {}
        for child, kind in listing(path).items():
            out[child] = kind
            if kind == 'FOLDER':
                out.update(tree(child))
        return out

    def describe(path):
        parts = []
        for child, kind in sorted(tree(path).items()):
            st, body = read(child)
            shown = body.decode(errors='replace') if isinstance(body, bytes) else body
            parts.append(f'{child[len(path):] or "."}:{kind}:GET={st}:{shown}')
        return '[' + ', '.join(parts) + ']'

    def start_mount(rec):
        """Mount the probe root with --write; returns (process, mountpoint)."""
        import subprocess
        import tempfile
        mnt = tempfile.mkdtemp(prefix='aidpfs-mnt-')
        proc = subprocess.Popen([a.mount_binary, '--profile', a.profile, '--region', a.region,
                                 '--platform-id', plats[0]['id'], '--workspace-key', wkey,
                                 '--remote-path', root, '--mount-point', mnt, '--write', '--recovery-dir', rec],
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        for _ in range(100):
            if os.path.ismount(mnt):
                return proc, mnt
            if proc.poll() is not None:
                raise RuntimeError('mount exited: ' + proc.stdout.read()[-500:])
            time.sleep(0.2)
        proc.kill()
        raise RuntimeError('mount timeout')

    def stop_mount(proc, mnt, lazy=False):
        import subprocess
        subprocess.run(['fusermount3', '-u'] + (['-z'] if lazy else []) + [mnt], check=False, capture_output=True)
        try:
            out, _ = proc.communicate(timeout=30)
        except subprocess.TimeoutExpired:
            proc.kill()
            out, _ = proc.communicate()
        for _ in range(50):
            if not os.path.ismount(mnt):
                break
            time.sleep(0.2)
        if not os.path.ismount(mnt):
            os.rmdir(mnt)
        return proc.returncode, (out or '').strip()

    def record(name, ok, detail=''):
        results.append((name, ok, detail))
        log(f'RESULT {name}: {"OK" if ok else "UNEXPECTED"} {detail}')

    try:
        if root in listing('/Workspace/Shared'):
            raise RuntimeError('root exists')
        log('root create')
        create(root, 'FOLDER')
        assert listing(root) == {}

        # A. Names: API name rules on create and on rename targets.
        if 'A' in a.cases:
            log('A names')
            dst_dir = root + '/dst dir'
            create(dst_dir, 'FOLDER')
            names = ['sp ace.txt', 'plus+x.txt', 'at@x.txt', 'under_score-x.txt', '.leading-dot.txt',
                     'trailing.dot.', 'x' * 200 + '.txt', '日本語.txt', 'hash#x.txt', 'paren(x).txt']
            for i, n in enumerate(names):
                src = root + '/' + n
                payload = f'aidpfs-name-{i}'.encode()
                try:
                    create(src, 'FILE', payload)
                except RuntimeError as e:
                    record('A create ' + n[:40], True, 'rejected: ' + str(e))
                    continue
                listed = src in listing(root)
                st, body = read(src)
                dst = dst_dir + '/r-' + n
                r = rename('rename ' + n[:40], src, dst)
                if r.status_code == 200:
                    moved(src, dst)
                cur = dst if r.status_code == 200 else src
                new_st, new_body = read(cur)
                d = req('delete ' + n[:40], 'DELETE', contents_url(cur))
                if d.status_code == 204:
                    owned_files.discard(cur)
                gone = cur not in listing(dst_dir if r.status_code == 200 else root)
                ok = listed and st == 200 and body == payload and r.status_code == 200 and new_st == 200 \
                    and new_body == payload and d.status_code == 204 and gone
                record('A ' + n[:40], ok, f'listed={listed} read={st}/{body == payload} rename={r.status_code} '
                       f'after={new_st}/{new_body == payload} delete={d.status_code} gone={gone}')
            # Rename a valid FILE to names that create rejects.
            for i, n in enumerate(['日本語-r.txt', 'hash#r.txt', 'paren(r).txt', 'slash%2Fr.txt']):
                src = root + f'/valid-{i}.txt'
                create(src, 'FILE', b'aidpfs-valid')
                dst = root + '/' + n
                r = rename('rename to ' + n, src, dst)
                lst = listing(root)
                if r.status_code == 200:
                    moved(src, dst)
                record('A rename-to ' + n, True, f'rename={r.status_code} src_listed={src in lst} dst_listed={dst in lst} '
                       f'dst_get={read(dst)[0]} tree={describe(root)[:300] if r.status_code == 200 else "-"}')

        # B. Case-only rename.
        if 'B' in a.cases:
            log('B case-only rename')
            src, dst = root + '/case.txt', root + '/CASE.txt'
            create(src, 'FILE', b'aidpfs-case')
            r = rename('case rename', src, dst)
            if r.status_code == 200:
                moved(src, dst)
            lst = listing(root)
            record('B case-only', True, f'rename={r.status_code} src_listed={src in lst} dst_listed={dst in lst} read_dst={read(dst)[0]}')

        # C. Destination already exists, each subcase isolated in its own folder.
        if 'C' in a.cases:
            log('C existing destination')
            def nb(path):
                r = req('create notebook', 'PUT', contents_url(path), headers={'Content-Type': 'application/json'},
                        data=json.dumps({'type': 'notebook', 'format': 'json',
                        'content': {'cells': [], 'metadata': {'aidpfs_marker': 'dest'}, 'nbformat': 4, 'nbformat_minor': 5}}))
                if not r.ok:
                    raise RuntimeError('notebook create failed')
                owned_files.add(path)
            subcases = [
                ('file->empty folder', 'FILE', 'FOLDER', False),
                ('file->nonempty folder', 'FILE', 'FOLDER', True),
                ('folder->file', 'FOLDER', 'FILE', False),
                ('file->notebook', 'FILE', 'NOTEBOOK', False),
                ('file->file', 'FILE', 'FILE', False),
                ('folder->empty folder', 'FOLDER', 'FOLDER', False),
                ('folder->nonempty folder', 'FOLDER', 'FOLDER', True),
            ]
            for idx, (label, sk, dk, dchild) in enumerate(subcases):
                if idx < a.c_from:
                    continue
                case = root + f'/c{idx}'
                create(case, 'FOLDER')
                src = case + ('/src.txt' if sk == 'FILE' else '/srcdir')
                dst = case + {'FILE': '/dst.txt', 'FOLDER': '/dstdir', 'NOTEBOOK': '/dst.ipynb'}[dk]
                if sk == 'FILE':
                    create(src, 'FILE', b'aidpfs-src')
                else:
                    create(src, 'FOLDER')
                    create(src + '/src-child.txt', 'FILE', b'aidpfs-src-child')
                if dk == 'NOTEBOOK':
                    nb(dst)
                elif dk == 'FILE':
                    create(dst, 'FILE', b'aidpfs-dst')
                else:
                    create(dst, 'FOLDER')
                    if dchild:
                        create(dst + '/dst-child.txt', 'FILE', b'aidpfs-dst-child')
                before = describe(case)
                r = rename(label, src, dst)
                time.sleep(1)
                after = describe(case)
                record('C ' + label, r.status_code == 409 and before == after, f'status={r.status_code} before={before} after={after}')

        # H. Read paths without an extension and folders.
        if 'H' in a.cases:
            log('H reads')
            for n in ['noext', 'Makefile', 'dot.txt']:
                pth = root + '/' + n
                create(pth, 'FILE', b'aidpfs-' + n.encode())
                for q in ({'content': 1}, {'content': 0}, {'content': 0, 'type': 'file'}, {'content': 1, 'type': 'file'}):
                    r = req(f'GET {n} {q}', 'GET', contents_url(pth), params=q)
                    j = r.json() if r.ok else {}
                    record(f'H {n} {q}', True, f'status={r.status_code} type={j.get("type")} format={j.get("format")} name={j.get("name")} mimetype={j.get("mimetype")}')
            folder = root + '/plainfolder'
            create(folder, 'FOLDER')
            for q in ({'content': 1}, {'content': 0}, {'content': 1, 'type': 'directory'}):
                r = req(f'GET folder {q}', 'GET', contents_url(folder), params=q)
                record(f'H folder {q}', True, f'status={r.status_code}')

        # I. Binary content with type=file, and non-ASCII Latin names.
        if 'I' in a.cases:
            log('I binary and latin names')
            for n, data in [('bin.dat', bytes(range(256))), ('binnoext', b'\xff\xfe\x00binary'), ('text.txt', b'plain text\n')]:
                pth = root + '/' + n
                create(pth, 'FILE', data)
                for q in ({'content': 1}, {'content': 1, 'type': 'file'}):
                    r = session.get(contents_url(pth), params=q, timeout=30)
                    m = r.json() if r.ok else {}
                    raw = m.get('content')
                    got = (base64.b64decode(raw) if m.get('format') == 'base64' else raw.encode()) if isinstance(raw, str) else None
                    record(f'I {n} {q}', True, f'status={r.status_code} format={m.get("format")} mimetype={m.get("mimetype")} equal={got == data}')
            for n in ['café.txt', 'ÄÖ.txt', 'ß.txt']:
                pth = root + '/' + n
                try:
                    create(pth, 'FILE', b'x')
                    record('I create ' + n, True, 'accepted')
                except RuntimeError as e:
                    record('I create ' + n, True, 'rejected ' + str(e))

        # R. Representative Git operations on a write mount.
        if 'R' in a.cases:
            log('R git')
            import subprocess
            import tempfile
            if not a.mount_binary:
                raise RuntimeError('case R requires --mount-binary')
            rec = tempfile.mkdtemp(prefix='aidpfs-rec-')
            os.chmod(rec, 0o700)
            proc, mnt = start_mount(rec)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL='/dev/null', GIT_TERMINAL_PROMPT='0')
            ident = ['-c', 'user.name=aidpfs probe', '-c', 'user.email=aidpfs-probe@example.invalid',
                     '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null']
            def run(args, cwd):
                r = subprocess.run(args, cwd=cwd, env=env, capture_output=True, timeout=120)
                return r.returncode, (r.stdout + r.stderr).decode(errors='replace').strip().replace('\n', ' | ')[:300]
            try:
                wt = os.path.join(mnt, 'wt')
                os.mkdir(wt)
                os.mkdir(os.path.join(wt, 'sub'))
                for n, data in [('a.txt', b'alpha\n'), ('Makefile', b'all:\n'), ('sub/b.txt', b'beta\n')]:
                    with open(os.path.join(wt, n), 'wb') as f:
                        f.write(data)
                        f.flush()
                        os.fsync(f.fileno())
                with tempfile.TemporaryDirectory(prefix='aidpfs-gitdir-') as gd:
                    run(['git', 'init', '--bare', '--quiet', gd], '/')
                    g = lambda *x: run(['git', '--git-dir', gd, '--work-tree', wt] + ident + list(x), wt)
                    record('R add+commit', g('add', '-A')[0] == 0 and g('commit', '-q', '-m', 'init')[0] == 0, g('log', '--oneline')[1])
                    with open(os.path.join(wt, 'a.txt'), 'wb') as f:
                        f.write(b'changed\n')
                        f.flush()
                        os.fsync(f.fileno())
                    code, out = g('diff', '--stat')
                    record('R diff sees change', code == 0 and 'a.txt' in out, out)
                    code, out = g('checkout', '--', 'a.txt')
                    st, body = read(root + '/wt/a.txt')
                    record('R checkout restores file', code == 0 and body == b'alpha\n', f'code={code} api={st}/{body!r} out={out}')
                    code, out = g('mv', 'a.txt', 'sub/c.txt')
                    record('R git mv', code == 0 and read(root + '/wt/sub/c.txt')[1] == b'alpha\n' and read(root + '/wt/a.txt')[0] == 404, f'code={code} out={out}')
                    code, out = g('rm', '-q', 'sub/b.txt')
                    record('R git rm', code == 0 and (root + '/wt/sub/b.txt') not in listing(root + '/wt/sub'), f'code={code} out={out}')
                    code, out = g('commit', '-q', '-m', 'move and remove')
                    s2 = g('status', '--porcelain')
                    record('R commit and clean status', code == 0 and s2 == (0, ''), f'code={code} status={s2}')
                    code, out = g('stash', 'list')
                    record('R stash list', code == 0, out)
                # Repository stored inside the mount: Git replaces files through
                # lockfile renames, which the mount does not support.
                repo = os.path.join(mnt, 'repo')
                os.mkdir(repo)
                code, out = run(['git', 'init', '-q'], repo)
                record('R in-mount git init', True, f'code={code} out={out}')
                if code == 0:
                    with open(os.path.join(repo, 'f.txt'), 'wb') as f:
                        f.write(b'x\n')
                    code, out = run(['git'] + ident + ['add', 'f.txt'], repo)
                    record('R in-mount git add', True, f'code={code} out={out}')
                    code, out = run(['git'] + ident + ['commit', '-q', '-m', 'x'], repo)
                    record('R in-mount git commit', True, f'code={code} out={out}')
                # The mount must still accept work after these failures.
                with open(os.path.join(mnt, 'after-git.txt'), 'wb') as f:
                    f.write(b'ok')
                    f.flush()
                    os.fsync(f.fileno())
                record('R mount usable afterwards', read(root + '/after-git.txt')[1] == b'ok', '')
            finally:
                code, out = stop_mount(proc, mnt)
                record('R unmount', True, f'code={code} output={out[-300:]!r} recovery={sorted(os.listdir(rec))}')

        # T. Children of dot-named folders, created right after the parent.
        if 'T' in a.cases:
            log('T dot folders')
            for parent in ['.hidden', 'visible', '.git']:
                pp = root + '/' + parent
                create(pp, 'FOLDER')
                for child, kind in [('info', 'FOLDER'), ('HEAD', 'FILE'), ('x.txt', 'FILE')]:
                    cp = pp + '/' + child
                    h = {'path': cp.encode(), 'type': kind, 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}
                    r = req(f'create {parent}/{child}', 'POST', base + '/objects', headers=h, data=b'')
                    if r.status_code in (200, 201):
                        (owned_folders.append(cp) if kind == 'FOLDER' else owned_files.add(cp))
                    lst = listing(pp)
                    record(f'T {parent}/{child} {kind}', True, f'status={r.status_code} listed={cp in lst}')
                r = req(f'dup create {parent}', 'POST', base + '/objects', headers={'path': pp.encode(), 'type': 'FOLDER', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=b'')
                record(f'T duplicate mkdir {parent}', True, f'status={r.status_code}')

        # U. contents GET metadata for folders with dotted names.
        if 'U' in a.cases:
            log('U folder GET by name shape')
            for n in ['.git', '.hidden', 'dir.d', 'v1.2', 'plain']:
                pp = root + '/' + n
                create(pp, 'FOLDER')
                for q in ({'content': 0}, {'content': 0, 'type': 'directory'}):
                    r = req(f'GET {n} {q}', 'GET', contents_url(pp), params=q)
                    j = r.json() if r.ok else {}
                    record(f'U {n} {q}', True, f'status={r.status_code} type={j.get("type")}')

        # S. Diagnose an in-mount git init.
        if 'S' in a.cases:
            log('S in-mount git init diagnosis')
            import subprocess
            import tempfile
            rec = tempfile.mkdtemp(prefix='aidpfs-rec-')
            os.chmod(rec, 0o700)
            proc, mnt = start_mount(rec)
            try:
                repo = os.path.join(mnt, 'repo')
                os.mkdir(repo)
                env = dict(os.environ, GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL='/dev/null', GIT_TRACE='1')
                r = subprocess.run(['strace', '-f', '-e', 'trace=%file', '-o', '/tmp/git-init.strace', 'git', 'init', '-q'], cwd=repo, env=env, capture_output=True, timeout=120) \
                    if subprocess.run(['which', 'strace'], capture_output=True).returncode == 0 else \
                    subprocess.run(['git', 'init', '-q'], cwd=repo, env=env, capture_output=True, timeout=120)
                log('git init rc', r.returncode, (r.stdout + r.stderr).decode(errors='replace')[-800:])
                if os.path.exists('/tmp/git-init.strace'):
                    lines = [l for l in open('/tmp/git-init.strace') if '/repo' in l and ('= -1' in l)]
                    log('failed syscalls:')
                    for l in lines[:40]:
                        log('   ', l.strip().replace(mnt, '<mnt>')[:200])
                for name, kind in sorted(tree(root + '/repo').items()):
                    log('  remote', kind, name.replace(root, '<root>'))
                lr = subprocess.run([a.mount_binary, 'recovery', 'list', '--dir', rec], capture_output=True, text=True, timeout=60)
                log('recovery list:', lr.stdout.replace(root, '<root>')[-1500:], lr.stderr[-300:])
            finally:
                code, out = stop_mount(proc, mnt)
                record('S unmount', True, f'code={code} output={out[-300:]!r}')

        # K. Process stop after a rename intent is durable.
        if 'K' in a.cases:
            log('K kill during rename')
            import signal as sig
            import subprocess
            import tempfile
            import threading
            if not a.mount_binary:
                raise RuntimeError('case K requires --mount-binary')
            outcomes = []
            for rnd in range(a.race_rounds):
                src, dst = root + f'/kill{rnd}.txt', root + f'/kill{rnd}-renamed.txt'
                create(src, 'FILE', f'aidpfs-kill-{rnd}'.encode())
                rec = tempfile.mkdtemp(prefix='aidpfs-rec-')
                os.chmod(rec, 0o700)
                proc, mnt = start_mount(rec)
                os.stat(os.path.join(mnt, f'kill{rnd}.txt'))
                result = {}
                def do_rename():
                    try:
                        os.rename(os.path.join(mnt, f'kill{rnd}.txt'), os.path.join(mnt, f'kill{rnd}-renamed.txt'))
                        result['errno'] = 0
                    except OSError as e:
                        result['errno'] = e.errno
                t = threading.Thread(target=do_rename, daemon=True)
                t.start()
                killed_after_intent = False
                for _ in range(2000):
                    if any(n.startswith('mutation-') and n.endswith('.json') for n in os.listdir(rec)):
                        proc.send_signal(sig.SIGKILL)
                        killed_after_intent = True
                        break
                    if not t.is_alive():
                        break
                    time.sleep(0.001)
                if not killed_after_intent:
                    proc.send_signal(sig.SIGKILL)
                t.join(timeout=30)
                code, _ = stop_mount(proc, mnt, lazy=True)
                time.sleep(3)
                lst = listing(root)
                where = 'source' if src in lst else ('destination' if dst in lst else 'missing')
                if where == 'destination':
                    moved(src, dst)
                files = sorted(os.listdir(rec))
                lr = subprocess.run([a.mount_binary, 'recovery', 'list', '--dir', rec], capture_output=True, text=True, timeout=60)
                ids = [n[len('mutation-'):-len('.json')] for n in files if n.startswith('mutation-') and n.endswith('.json')]
                insp = ''
                if ids:
                    ir = subprocess.run([a.mount_binary, 'recovery', 'inspect-mutation', '--dir', rec, '--id', ids[0],
                                         '--region', a.region, '--profile', a.profile], capture_output=True, text=True, timeout=60)
                    insp = (ir.stdout + ir.stderr).strip()
                # A new mount must refuse a non-empty recovery directory.
                p2 = subprocess.run([a.mount_binary, '--profile', a.profile, '--region', a.region,
                                     '--platform-id', plats[0]['id'], '--workspace-key', wkey, '--remote-path', root,
                                     '--mount-point', tempfile.mkdtemp(prefix='aidpfs-mnt-'), '--write', '--recovery-dir', rec],
                                    capture_output=True, text=True, timeout=60)
                done_marker = any(n.endswith('.done') for n in files)
                record(f'K round {rnd}', killed_after_intent and bool(ids) and not done_marker and p2.returncode != 0,
                       f'killed_after_intent={killed_after_intent} rename_errno={result.get("errno")} remote={where} '
                       f'journal={files} list_rc={lr.returncode} inspect={insp[-250:]!r} remount_rc={p2.returncode} remount_msg={(p2.stdout + p2.stderr).strip()[-150:]!r}')
                outcomes.append(where)
            record('K remote outcomes', True, str(outcomes))

        # M. Real aidpfs --write mount of the probe root.
        if 'M' in a.cases:
            log('M aidpfs mount')
            if not a.mount_binary:
                raise RuntimeError('case M requires --mount-binary')
            import ctypes
            import errno as E
            import subprocess
            import tempfile
            libc = ctypes.CDLL(None, use_errno=True)
            def renameat2(src, dst, flags):
                AT_FDCWD = -100
                if libc.renameat2(AT_FDCWD, src.encode(), AT_FDCWD, dst.encode(), flags) == 0:
                    return 0
                return ctypes.get_errno()
            def plain_rename(src, dst):
                try:
                    os.rename(src, dst)
                    return 0
                except OSError as e:
                    return e.errno
            create(root + '/Makefile', 'FILE', b'all:\n\techo ok\n')
            create(root + '/a.txt', 'FILE', b'aidpfs-a')
            create(root + '/b.txt', 'FILE', b'aidpfs-b')
            create(root + '/sub', 'FOLDER')
            mnt = tempfile.mkdtemp(prefix='aidpfs-mnt-')
            rec = tempfile.mkdtemp(prefix='aidpfs-rec-')
            os.chmod(rec, 0o700)
            proc = subprocess.Popen([a.mount_binary, '--profile', a.profile, '--region', a.region,
                                     '--platform-id', plats[0]['id'], '--workspace-key', wkey,
                                     '--remote-path', root, '--mount-point', mnt, '--write', '--recovery-dir', rec],
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            try:
                for _ in range(100):
                    if os.path.ismount(mnt):
                        break
                    if proc.poll() is not None:
                        raise RuntimeError('mount exited: ' + proc.stdout.read()[-500:])
                    time.sleep(0.2)
                else:
                    raise RuntimeError('mount timeout')
                m = lambda n: os.path.join(mnt, n)
                with open(m('Makefile'), 'rb') as f:
                    data = f.read()
                same = data == b'all:\n\techo ok\n'
                record('M read extension-less FILE', same, f'size={os.stat(m("Makefile")).st_size} equal={same}')
                got = plain_rename(m('a.txt'), m('b.txt'))
                record('M rename onto FILE', got == E.EOPNOTSUPP, E.errorcode.get(got, str(got)))
                got = renameat2(m('a.txt'), m('b.txt'), 1)
                record('M NOREPLACE onto FILE', got == E.EEXIST, E.errorcode.get(got, str(got)))
                got = plain_rename(m('a.txt'), m('sub'))
                record('M rename onto FOLDER', got in (E.EOPNOTSUPP, E.EISDIR), E.errorcode.get(got, str(got)))
                got = plain_rename(m('a.txt'), m('bad#name.txt'))
                record('M rename to invalid name', got == E.EINVAL, E.errorcode.get(got, str(got)))
                try:
                    open(m('bad(name).txt'), 'w').close()
                    got = 0
                except OSError as e:
                    got = e.errno
                record('M create invalid name', got == E.EINVAL, E.errorcode.get(got, str(got)))
                try:
                    os.mkdir(m('bad~dir'))
                    got = 0
                except OSError as e:
                    got = e.errno
                record('M mkdir invalid name', got == E.EINVAL, E.errorcode.get(got, str(got)))
                got = renameat2(m('a.txt'), m('sub/c.txt'), 1)
                if got == 0:
                    moved(root + '/a.txt', root + '/sub/c.txt')
                st, body = read(root + '/sub/c.txt')
                record('M NOREPLACE rename after rejections', got == 0 and body == b'aidpfs-a' and read(root + '/a.txt')[0] == 404,
                       f'errno={E.errorcode.get(got, got)} api_new={st} equal={body == b"aidpfs-a"}')
                try:
                    os.unlink(m('b.txt'))
                    got = 0
                except OSError as e:
                    got = e.errno
                record('M unlink', got == 0 and (root + '/b.txt') not in listing(root), E.errorcode.get(got, str(got)))
                try:
                    os.rmdir(m('sub'))
                    got = 0
                except OSError as e:
                    got = e.errno
                record('M rmdir stays unsupported', got == E.EOPNOTSUPP and (root + '/sub') in listing(root), E.errorcode.get(got, str(got)))
            finally:
                subprocess.run(['fusermount3', '-u', mnt], check=False)
                try:
                    out, _ = proc.communicate(timeout=30)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    out, _ = proc.communicate()
                record('M unmount exit', proc.returncode == 0, f'code={proc.returncode} output={out.strip()[-300:]!r}')
                leftover = os.listdir(rec)
                record('M recovery dir', True, f'entries={sorted(leftover)}')
                if not os.path.ismount(mnt):
                    os.rmdir(mnt)

        # D. Missing source and missing destination parent.
        if 'D' in a.cases:
            log('D missing source / parent')
            r = rename('missing source rename', root + '/missing.txt', root + '/missing2.txt')
            d = req('missing source contents delete', 'DELETE', contents_url(root + '/missing.txt'))
            d2 = req('missing source objects delete', 'DELETE', base + '/objects/' + quote(inside(root + '/missing.txt'), safe=''))
            lst = listing(root)
            record('D missing source', root + '/missing2.txt' not in lst,
                   f'rename={r.status_code} contents_delete={d.status_code} objects_delete={d2.status_code}')
            f_src = root + '/parent-src.txt'
            create(f_src, 'FILE', b'aidpfs-parent-src')
            r = rename('missing parent rename', f_src, root + '/no-such-dir/x.txt')
            if r.status_code == 200:
                moved(f_src, root + '/no-such-dir/x.txt')
            lst = listing(root)
            record('D missing parent', True, f'rename={r.status_code} src_kept={f_src in lst} parent_created={(root + "/no-such-dir") in lst}')
            if (root + '/no-such-dir') in lst and (root + '/no-such-dir') not in owned_folders:
                owned_folders.append(root + '/no-such-dir')

        # E. Folder rename (empty and non-empty) and non-empty folder delete via objects API.
        if 'E' in a.cases:
            log('E folders')
            empty = root + '/empty-folder'
            create(empty, 'FOLDER')
            r = rename('empty folder rename', empty, root + '/empty-folder-renamed')
            if r.status_code == 200:
                owned_folders[owned_folders.index(empty)] = root + '/empty-folder-renamed'
            lst = listing(root)
            record('E empty folder rename', True, f'rename={r.status_code} old={empty in lst} new={lst.get(root + "/empty-folder-renamed")}')
            full = root + '/full-folder'
            create(full, 'FOLDER')
            child = full + '/child.txt'
            create(child, 'FILE', b'aidpfs-child')
            newfull = root + '/full-folder-renamed'
            r = rename('nonempty folder rename', full, newfull)
            if r.status_code == 200:
                owned_folders[owned_folders.index(full)] = newfull
                moved(child, newfull + '/child.txt')
                child = newfull + '/child.txt'
                full = newfull
            record('E nonempty folder rename', True, f'rename={r.status_code} child_at_new={read(child)} children={list(listing(full))}')
            d = req('nonempty folder objects delete', 'DELETE', base + '/objects/' + quote(inside(full), safe=''))
            time.sleep(2)
            lst = listing(root)
            child_state = read(child)
            if full not in lst or child_state[0] != 200:
                owned_files.discard(child)
                if full not in lst:
                    owned_folders.remove(full)
            record('E nonempty folder objects DELETE', True,
                   f'status={d.status_code} work_request={d.headers.get("opc-work-request-id") is not None} folder_listed={full in lst} child={child_state[0]}')
            # Empty folder via objects DELETE.
            e2 = root + '/empty-objects-delete'
            create(e2, 'FOLDER')
            d = req('empty folder objects delete', 'DELETE', base + '/objects/' + quote(inside(e2), safe=''))
            time.sleep(2)
            gone = e2 not in listing(root)
            if gone:
                owned_folders.remove(e2)
            record('E empty folder objects DELETE', True, f'status={d.status_code} gone={gone}')

        # F. Concurrent rename of two sources to one absent destination.
        if 'F' in a.cases:
            log('F concurrent rename')
            for rnd in range(a.race_rounds):
                srcs = [root + f'/race{rnd}-a.txt', root + f'/race{rnd}-b.txt']
                dst = root + f'/race{rnd}-dest.txt'
                for k, s in enumerate(srcs):
                    create(s, 'FILE', f'aidpfs-race-{rnd}-{k}'.encode())
                with concurrent.futures.ThreadPoolExecutor(2) as ex:
                    futs = [ex.submit(lambda s=s: (s, rename('race rename', s, dst, timeout=60))) for s in srcs]
                    outs = []
                    for fu in futs:
                        try:
                            outs.append(fu.result())
                        except Unknown:
                            outs.append((None, None))
                if any(o[1] is None for o in outs):
                    unknown.append('race round %d' % rnd)
                lst = listing(root)
                st, body = read(dst)
                survivors = [s for s in srcs if s in lst]
                winner = [s for s, rr in outs if rr is not None and rr.status_code == 200]
                for s in srcs:
                    if s not in lst:
                        owned_files.discard(s)
                if dst in lst:
                    owned_files.add(dst)
                expected = len(winner) == 1 and len(survivors) == 1 and body == f'aidpfs-race-{rnd}-{srcs.index(winner[0])}'.encode()
                record(f'F race {rnd}', expected, f'statuses={[rr.status_code if rr is not None else "EXC" for _, rr in outs]} '
                       f'survivors={len(survivors)} dest={st}')

        # G. Response lost: tiny read timeout after sending.
        if 'G' in a.cases:
            log('G lost response')
            src, dst = root + '/lost.txt', root + '/lost-renamed.txt'
            create(src, 'FILE', b'aidpfs-lost')
            try:
                rename('lost rename', src, dst, timeout=(10, 0.001))
                record('G lost response', False, 'response arrived before timeout')
            except Unknown:
                time.sleep(3)
                lst = listing(root)
                if dst in lst:
                    moved(src, dst)
                record('G lost response', True, f'after timeout src_listed={src in lst} dst_listed={dst in lst}')
    except Unknown as e:
        unknown.append(str(e))
    finally:
        log('cleanup')
        if unknown:
            log('UNKNOWN outcomes, cleanup stopped:', unknown, 'journal kept at', a.journal)
        else:
            try:
                # Every entry under the fresh UUID root was created by this run.
                # Delete leaves individually, deepest first; never recursive.
                def delete_present(path, parent):
                    for attempt in range(6):
                        r = req('cleanup', 'DELETE', contents_url(path))
                        if r.status_code == 204:
                            return
                        time.sleep(5)
                        if path not in listing(parent):
                            return
                    raise RuntimeError('cleanup delete failed: ' + path)
                entries = tree(root)
                for path, kind in sorted(entries.items(), key=lambda x: -x[0].count('/')):
                    if kind != 'FOLDER':
                        delete_present(path, path.rsplit('/', 1)[0])
                for path, kind in sorted(entries.items(), key=lambda x: -x[0].count('/')):
                    if kind == 'FOLDER':
                        if listing(path):
                            raise RuntimeError('folder not empty: ' + path)
                        delete_present(path, path.rsplit('/', 1)[0])
                if listing(root):
                    raise RuntimeError('root not empty')
                delete_present(root, '/Workspace/Shared')
                if root not in listing('/Workspace/Shared'):
                    log('root gone: True')
                    pathlib.Path(a.journal).unlink()
                else:
                    log('root still listed; journal kept')
            except Exception as e:
                log('cleanup error', type(e).__name__, e, 'journal kept at', a.journal)
        log('SUMMARY')
        for name, ok, detail in results:
            log(f'  {"OK " if ok else "NG "} {name}: {detail}')


if __name__ == '__main__':
    main()
