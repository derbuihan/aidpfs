#!/usr/bin/env python3
"""Isolated AIDP write probe. Requires OCI Python SDK, requests and explicit IDs.

Only creates a randomly named directory under /Workspace/Shared. An interrupted
run leaves a mode-0600 journal; cleanup never recursively removes unknown entries.
"""
import argparse
import errno
import base64
import json
import hashlib
import os
import pathlib
import signal
import shutil
import subprocess
import tempfile
import time
import sys
import uuid
from urllib.parse import quote, urlsplit

import oci
import requests


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--platform-id', required=True)
    p.add_argument('--workspace-key', required=True)
    p.add_argument('--profile', default='OCI_TOKYO')
    p.add_argument('--endpoint', default='https://aidp.ap-osaka-1.oci.oraclecloud.com')
    p.add_argument('--journal', required=True, help='new private recovery journal path')
    p.add_argument('--mount-binary', help='optional aidpfs executable for live mount checks')
    p.add_argument('--write-mount-binary', help='optional aidpfs executable for isolated experimental write checks')
    p.add_argument('--region', default='ap-osaka-1')
    p.add_argument('--go-par-test', action='store_true', help='run Go live PAR integration against this run only')
    a = p.parse_args()
    if not a.endpoint.startswith('https://') or '/' in a.endpoint[8:]:
        p.error('endpoint must be an HTTPS origin')
    token = uuid.uuid4().hex
    root = '/Workspace/Shared/aidpfs-probe-' + token
    journal = pathlib.Path(a.journal)
    fd = os.open(journal, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as f:
        json.dump({'root': root, 'platform_id': a.platform_id, 'workspace_key': a.workspace_key,
                   'endpoint': a.endpoint, 'profile': a.profile}, f)
        f.flush()
        os.fsync(f.fileno())
    c = oci.config.from_file(profile_name=a.profile)
    signer = oci.signer.Signer(tenancy=c['tenancy'], user=c['user'], fingerprint=c['fingerprint'],
                               private_key_file_location=c['key_file'], pass_phrase=c.get('pass_phrase'))
    base = a.endpoint + '/20260430/aiDataPlatforms/' + quote(a.platform_id, safe='') + '/workspaces/' + quote(a.workspace_key, safe='')
    session = requests.Session()
    session.auth = signer
    def request(method, url, **kwargs):
        response = session.request(method, url, timeout=20, allow_redirects=False, **kwargs)
        print(method, response.status_code, 'request-id', response.headers.get('opc-request-id'), flush=True)
        return response
    def contents(path):
        if not (path == root or path.startswith(root + '/')):
            raise ValueError('path outside probe root')
        return base + '/notebook/api/contents/' + quote(path.lstrip('/'), safe='')
    def listing(path):
        items = []
        page = None
        seen = set()
        while True:
            q = {'path': path, 'limit': 1000}
            if page: q['page'] = page
            r = request('GET', base + '/objects', params=q)
            r.raise_for_status()
            items.extend(r.json()['items'])
            page = r.headers.get('opc-next-page')
            if not page: return items
            if page in seen: raise RuntimeError('repeated page token')
            seen.add(page)
    def stop(_signum, _frame):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, stop)
    created_root = False
    mount_dir = None
    mount_process = None
    open_handle = None
    owned = set()
    nested_pending = False
    try:
        if any(x.get('path') == root for x in listing('/Workspace/Shared')):
            raise RuntimeError('random probe root already exists')
        r = request('POST', base + '/objects', headers={'path': root, 'type': 'FOLDER', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=b'')
        if r.status_code not in (200, 201):
            raise RuntimeError('probe root creation failed or ambiguous; inspect journal')
        created_root = True
        print('root created; empty listing:', len(listing(root)) == 0, flush=True)
        # Determine whether contents DELETE rejects a non-empty directory.
        # Both directory and child are generated under the isolated root.
        nested = root + '/nonempty-test'
        nested_child = nested + '/child.txt'
        nested_pending = True
        folder_response = request('POST', base + '/objects', headers={'path': nested, 'type': 'FOLDER', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=b'')
        if folder_response.status_code not in (200, 201): raise RuntimeError('nested folder creation failed or ambiguous')
        owned.add(nested)
        child_response = request('POST', base + '/objects', headers={'path': nested_child, 'type': 'FILE', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=b'synthetic')
        if child_response.status_code not in (200, 201): raise RuntimeError('nested child creation failed or ambiguous')
        if {x.get('path') for x in listing(nested)} != {nested_child}: raise RuntimeError('unexpected nested contents')
        deletion = request('DELETE', contents(nested))
        parent_items = {x.get('path') for x in listing(root)}
        folder_remains = nested in parent_items
        child_remains = None
        if folder_remains:
            nested_items = listing(nested)
            if any(x.get('path') != nested_child for x in nested_items): raise RuntimeError('unexpected nested contents after deletion')
            child_remains = any(x.get('path') == nested_child for x in nested_items)
            if child_remains:
                r = request('DELETE', contents(nested_child))
                if r.status_code not in (200, 204): raise RuntimeError('nested child cleanup failed')
            if listing(nested): raise RuntimeError('nested folder not empty')
            r = request('DELETE', contents(nested))
            if r.status_code not in (200, 204): raise RuntimeError('nested folder cleanup failed')
        else:
            orphan_items = listing(nested)
            orphan_read = request('GET', contents(nested_child), params={'content': 1})
            print('deleted folder child listing count', len(orphan_items), 'child GET status', orphan_read.status_code, flush=True)
            if orphan_items or orphan_read.status_code != 404:
                raise RuntimeError('possible orphaned nested content; inspect private journal')
        print('nonempty folder DELETE status', deletion.status_code, 'folder remains', folder_remains,
              'child remains', child_remains, 'request-id', deletion.headers.get('opc-request-id'), flush=True)
        if any(x.get('path') == nested for x in listing(root)): raise RuntimeError('nested folder still present')
        owned.discard(nested)
        nested_pending = False
        # Probe delete preconditions only on separately created synthetic files.
        for label in ('contents', 'objects'):
            delete_path = root + '/conditional-delete-' + label + '.bin'
            created_delete = request('POST', base + '/objects', headers={'path': delete_path, 'type': 'FILE', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=b'synthetic-delete-probe')
            if created_delete.status_code not in (200, 201): raise RuntimeError('conditional delete fixture creation failed or ambiguous')
            owned.add(delete_path)
            endpoint = contents(delete_path) if label == 'contents' else base + '/objects/' + quote(delete_path, safe='')
            deleted = request('DELETE', endpoint, headers={'if-match': '"aidpfs-obviously-wrong-etag"'})
            remaining = any(item.get('path') == delete_path for item in listing(root))
            check = request('GET', contents(delete_path), params={'content': 1})
            preserved = False
            if check.ok:
                model = check.json()
                observed = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
                preserved = observed == b'synthetic-delete-probe'
            print('conditional delete', label, 'status', deleted.status_code, 'listed', remaining,
                  'GET status', check.status_code, 'original preserved', preserved, flush=True)
        file_path = root + '/binary.bin'
        data = bytes([0, 1, 2, 127, 128, 255])
        r = request('POST', base + '/objects', headers={'path': file_path, 'type': 'FILE', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=data)
        if r.status_code not in (200, 201): raise RuntimeError('file create failed')
        owned.add(file_path)
        first_tag = r.headers.get('etag')
        print('file create ETag present', bool(first_tag), flush=True)
        object_url = base + '/objects/' + quote(file_path, safe='')
        for method in ('HEAD', 'GET'):
            meta = request(method, object_url)
            print('object', method, 'ETag present', bool(meta.headers.get('etag')), flush=True)
        listed = listing(root)
        match = [x for x in listed if x.get('path') == file_path]
        print('listing compositeEtag present', bool(match and match[0].get('compositeEtag')), flush=True)
        r = request('POST', base + '/objects', headers={'path': file_path, 'type': 'FILE', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=b'other')
        print('duplicate create rejected', r.status_code >= 400, flush=True)
        r = request('GET', contents(file_path), params={'content': 1})
        if r.ok:
            obj = r.json()
            got = base64.b64decode(obj['content']) if obj.get('format') == 'base64' else obj.get('content', '').encode()
            print('file contents format', obj.get('format'), 'bytes match', got == data, 'ETag present', bool(r.headers.get('etag')), flush=True)
        # Generating a PAR is a POST with side effects, confined to our test file.
        par_response = request('POST', base + '/actions/downloadFileMeta',
            params={'shouldGenerateNewPar': 'true'}, headers={'path': file_path, 'type': 'FILE'})
        if par_response.ok:
            par_meta = par_response.json()
            par_url = par_meta.get('parUrl')
            print('download PAR present', bool(par_url), 'metadata ETag present', bool(par_meta.get('eTag')),
                  'metadata size matches', par_meta.get('size') == len(data), flush=True)
            if par_url:
                parsed = urlsplit(par_url)
                expected_host = 'objectstorage.' + a.region + '.oraclecloud.com'
                if parsed.scheme != 'https' or parsed.hostname != expected_host or parsed.username or parsed.password or parsed.port:
                    raise RuntimeError('PAR URL origin is not expected OCI Object Storage')
                try:
                    with requests.Session() as par_client:
                        par_client.trust_env = False
                        download = par_client.get(par_url, timeout=20, allow_redirects=False)
                        print('PAR download status', download.status_code, 'auth absent', 'Authorization' not in download.request.headers,
                              'binary match', download.status_code == 200 and download.content == data, flush=True)
                except requests.RequestException as exc:
                    print('PAR transfer failed', type(exc).__name__, flush=True)
        # Isolated upload PAR: CREATE may already create a workspace object.
        upload_path = root + '/upload-par.bin'
        upload_bytes = bytes([23, 0, 255, 42, 128, 91, 13])
        upload_headers = {'path': upload_path, 'type': 'FILE'}
        put_etag = None
        created = request('POST', base + '/actions/uploadFileMeta',
            params={'isOverwrite': 'false', 'shouldCreateRecursively': 'false', 'shouldGenerateNewPar': 'true'},
            headers=upload_headers, json={'action': 'CREATE'})
        if created.status_code in (200, 201):
            owned.add(upload_path)
            created_meta = created.json()
            upload_url = created_meta.get('parUrl')
            print('upload PAR present', bool(upload_url), 'create metadata ETag present', bool(created_meta.get('eTag')),
                  'object visible before transfer', any(x.get('path') == upload_path for x in listing(root)), flush=True)
            if upload_url:
                parsed = urlsplit(upload_url)
                expected_host = 'objectstorage.' + a.region + '.oraclecloud.com'
                if parsed.scheme != 'https' or parsed.hostname != expected_host or parsed.username or parsed.password or parsed.port:
                    raise RuntimeError('upload PAR URL origin is not expected OCI Object Storage')
                try:
                    with requests.Session() as par_client:
                        par_client.trust_env = False
                        sent = par_client.put(upload_url, data=upload_bytes, timeout=20, allow_redirects=False)
                        put_etag = sent.headers.get('etag')
                        print('PAR upload status', sent.status_code, 'auth absent', 'Authorization' not in sent.request.headers,
                              'transfer ETag present', bool(put_etag), flush=True)
                    if sent.status_code in (200, 201, 204) and put_etag:
                        closed = request('POST', base + '/actions/uploadFileMeta', headers=upload_headers,
                            params={'shouldCreateRecursively': 'false'}, json={'action': 'UPDATE', 'eTag': put_etag, 'size': len(upload_bytes)})
                        print('upload close status', closed.status_code, flush=True)
                        if closed.ok:
                            verify = request('GET', contents(upload_path), params={'content': 1})
                            if verify.ok:
                                model = verify.json()
                                binary = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
                                print('closed upload bytes match', binary == upload_bytes, flush=True)
                except requests.RequestException as exc:
                    print('PAR upload transfer failed', type(exc).__name__, flush=True)
        else:
            print('upload PAR create did not succeed; unknown objects stop cleanup', flush=True)
        if a.write_mount_binary:
            if not put_etag: raise RuntimeError('experimental mount requires a completed synthetic upload')
            write_mount_dir = tempfile.mkdtemp(prefix='aidpfs-cli-mount-')
            recovery_dir = tempfile.mkdtemp(prefix='aidpfs-cli-recovery-')
            cli = subprocess.Popen([a.write_mount_binary, '--profile', a.profile, '--region', a.region,
                '--endpoint', a.endpoint, '--platform-id', a.platform_id, '--workspace-key', a.workspace_key,
                '--remote-path', root, '--mount-point', write_mount_dir, '--experimental-write', '--recovery-dir', recovery_dir],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            cli_success = False
            try:
                for _ in range(200):
                    if os.path.ismount(write_mount_dir) or cli.poll() is not None: break
                    time.sleep(0.1)
                if not os.path.ismount(write_mount_dir): raise RuntimeError('experimental CLI mount failed')
                created_path = root + '/cli-created.bin'
                with open(os.path.join(write_mount_dir, 'cli-created.bin'), 'xb', buffering=0) as new_file:
                    owned.add(created_path)
                    new_file.write(b'created-synthetic')
                    os.fsync(new_file.fileno())
                created_read = request('GET', contents(created_path), params={'content': 1})
                if not created_read.ok: raise RuntimeError('created file verification failed')
                model = created_read.json()
                created_data = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
                if created_data != b'created-synthetic': raise RuntimeError('created file content mismatch')
                try:
                    with open(os.path.join(write_mount_dir, 'cli-created.bin'), 'xb'): pass
                    raise RuntimeError('exclusive duplicate creation succeeded')
                except FileExistsError:
                    pass
                intent_files = [name for name in os.listdir(recovery_dir) if name.startswith('create-') and name.endswith('.json')]
                if len(intent_files) != 1: raise RuntimeError('create intent count mismatch')
                with open(os.path.join(recovery_dir, intent_files[0])) as intent_file:
                    intent = json.load(intent_file)
                if intent.get('remote_path') != created_path or not os.path.isfile(os.path.join(recovery_dir, intent_files[0][:-5] + '.done')):
                    raise RuntimeError('confirmed create intent missing')
                diagnostic = subprocess.run([a.write_mount_binary, 'recovery', 'inspect-create', '--dir', recovery_dir,
                    '--id', intent['id'], '--region', a.region, '--profile', a.profile, '--endpoint', a.endpoint],
                    capture_output=True, text=True, timeout=60)
                if diagnostic.returncode or 'confirmed=true observation=file_listed' not in diagnostic.stdout or created_path in diagnostic.stdout:
                    raise RuntimeError('create intent diagnosis failed')
                print('experimental CLI create intent diagnosis succeeded', flush=True)
                print('experimental CLI create, write, fsync and duplicate rejection succeeded', flush=True)
                created_dir = root + '/cli-dir'
                os.mkdir(os.path.join(write_mount_dir, 'cli-dir'))
                owned.add(created_dir)
                if not os.path.isdir(os.path.join(write_mount_dir, 'cli-dir')): raise RuntimeError('created directory not visible')
                try:
                    os.mkdir(os.path.join(write_mount_dir, 'cli-dir'))
                    raise RuntimeError('duplicate mkdir succeeded')
                except FileExistsError:
                    pass
                dir_items = [entry for entry in listing(root) if entry.get('path') == created_dir and entry.get('type') == 'FOLDER']
                if len(dir_items) != 1: raise RuntimeError('created directory not listed')
                dir_intents = []
                for name in os.listdir(recovery_dir):
                    if name.startswith('create-') and name.endswith('.json'):
                        with open(os.path.join(recovery_dir, name)) as f: item = json.load(f)
                        if item.get('remote_path') == created_dir and item.get('kind') == 'FOLDER' and os.path.isfile(os.path.join(recovery_dir, name[:-5]+'.done')):
                            dir_intents.append(item)
                if len(dir_intents) != 1: raise RuntimeError('directory create intent missing')
                diagnostic = subprocess.run([a.write_mount_binary, 'recovery', 'inspect-create', '--dir', recovery_dir,
                    '--id', dir_intents[0]['id'], '--region', a.region, '--profile', a.profile, '--endpoint', a.endpoint],
                    capture_output=True, text=True, timeout=60)
                if diagnostic.returncode or 'confirmed=true observation=directory_listed' not in diagnostic.stdout: raise RuntimeError('directory diagnosis failed')
                print('experimental CLI mkdir, duplicate rejection and intent diagnosis succeeded', flush=True)
                child_path = created_dir + '/child.bin'
                with open(os.path.join(write_mount_dir, 'cli-dir', 'child.bin'), 'xb', buffering=0) as child_file:
                    child_file.write(b'nested-synthetic')
                    os.fsync(child_file.fileno())
                append_fd = os.open(os.path.join(write_mount_dir, 'cli-dir', 'child.bin'), os.O_WRONLY | os.O_APPEND | os.O_SYNC)
                try:
                    if os.write(append_fd, b'+') != 1: raise RuntimeError('nested append failed')
                finally:
                    os.close(append_fd)
                child_response = request('GET', contents(child_path), params={'content': 1})
                if not child_response.ok: raise RuntimeError('nested file read failed')
                model = child_response.json()
                child_data = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
                if child_data != b'nested-synthetic+': raise RuntimeError('nested file mismatch')
                for operation, target in ((os.unlink, os.path.join(write_mount_dir, 'cli-dir', 'child.bin')),
                                          (os.rmdir, os.path.join(write_mount_dir, 'cli-dir'))):
                    try:
                        operation(target)
                        raise RuntimeError('unsupported FUSE delete unexpectedly succeeded')
                    except OSError as exc:
                        if exc.errno != errno.EOPNOTSUPP: raise
                print('experimental CLI unlink and rmdir refused', flush=True)
                if {item.get('path') for item in listing(created_dir)} != {child_path}: raise RuntimeError('unexpected nested directory contents')
                child_intents = []
                for name in os.listdir(recovery_dir):
                    if name.startswith('create-') and name.endswith('.json'):
                        with open(os.path.join(recovery_dir, name)) as f: item = json.load(f)
                        if item.get('remote_path') == child_path and item.get('kind') == 'FILE': child_intents.append(item)
                if len(child_intents) != 1: raise RuntimeError('nested file intent missing')
                removed = request('DELETE', contents(child_path))
                if removed.status_code != 204 or listing(created_dir): raise RuntimeError('nested child cleanup failed')
                diagnostic = subprocess.run([a.write_mount_binary, 'recovery', 'inspect-create', '--dir', recovery_dir,
                    '--id', child_intents[0]['id'], '--region', a.region, '--profile', a.profile, '--endpoint', a.endpoint],
                    capture_output=True, text=True, timeout=60)
                if diagnostic.returncode or 'confirmed=true observation=not_listed' not in diagnostic.stdout: raise RuntimeError('removed child diagnosis failed')
                print('experimental CLI nested create, write, append, read, cleanup and not_listed diagnosis succeeded', flush=True)
                with open(os.path.join(write_mount_dir, 'upload-par.bin'), 'r+b', buffering=0) as handle:
                    handle.seek(0)
                    handle.write(b'Z')
                    os.fsync(handle.fileno())
                    check = request('GET', contents(upload_path), params={'content': 1})
                    if not check.ok: raise RuntimeError('experimental CLI verification failed')
                    model = check.json()
                    binary = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
                    if binary != b'Z' + upload_bytes[1:]: raise RuntimeError('experimental CLI write mismatch')
                    handle.seek(0)
                    handle.write(upload_bytes[:1])
                    os.fsync(handle.fileno())
                check = request('GET', contents(upload_path), params={'content': 1})
                if not check.ok: raise RuntimeError('experimental CLI restore verification failed')
                model = check.json()
                binary = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
                if binary != upload_bytes: raise RuntimeError('experimental CLI restore mismatch')
                refreshed_meta = request('POST', base + '/actions/downloadFileMeta', params={'shouldGenerateNewPar': 'true'}, headers=upload_headers)
                if not refreshed_meta.ok or not refreshed_meta.json().get('eTag'): raise RuntimeError('experimental CLI restore ETag unavailable')
                put_etag = refreshed_meta.json()['eTag']
                print('experimental CLI write, fsync, remote read and restore succeeded', flush=True)
                cli_success = True
            finally:
                if os.path.ismount(write_mount_dir):
                    result = subprocess.run(['fusermount3', '-u', write_mount_dir], capture_output=True, timeout=15)
                    if result.returncode: cli_success = False
                if cli.poll() is None:
                    cli.send_signal(signal.SIGTERM)
                    try: cli.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        cli.kill()
                        cli.wait()
                if os.path.ismount(write_mount_dir): raise RuntimeError('experimental CLI mount still active')
                os.rmdir(write_mount_dir)
                if cli_success:
                    shutil.rmtree(recovery_dir)
                else:
                    print('experimental CLI recovery directory retained', recovery_dir, flush=True)
                    raise RuntimeError('experimental CLI check failed')
        if a.go_par_test:
            if not put_etag: raise RuntimeError('Go PAR test requires a completed synthetic upload')
            delete_fixture = root + '/live-delete.bin'
            created_delete_fixture = request('POST', base + '/objects', headers={'path': delete_fixture, 'type': 'FILE', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=b'synthetic')
            if created_delete_fixture.status_code not in (200, 201): raise RuntimeError('delete fixture creation failed or ambiguous')
            owned.add(delete_fixture)
            env = os.environ.copy()
            env.update({'AIDPFS_LIVE_PAR_PROBE': 'isolated', 'AIDPFS_PROBE_ROOT': root,
                        'AIDPFS_PROBE_PLATFORM': a.platform_id, 'AIDPFS_PROBE_WORKSPACE': a.workspace_key,
                        'AIDPFS_PROBE_PROFILE': a.profile, 'AIDPFS_PROBE_REGION': a.region,
                        'AIDPFS_PROBE_ENDPOINT': a.endpoint})
            result = subprocess.run(['go', 'test', '-p=1', '-count=1', '-run', '^TestLive(PAR|FUSE)IsolatedFixture$', '-v', './internal/aidp', './internal/mount'], env=env, timeout=120)
            if result.returncode: raise RuntimeError('Go live PAR test failed')
            owned.add(root + '/go-created-empty.bin')
            # The Go test restores bytes, but the underlying object has a new ETag.
            updated_meta = request('POST', base + '/actions/downloadFileMeta',
                params={'shouldGenerateNewPar': 'true'}, headers=upload_headers)
            if not updated_meta.ok or not updated_meta.json().get('eTag'): raise RuntimeError('cannot re-read test ETag')
            put_etag = updated_meta.json()['eTag']
            # Another client writes after A's storage PUT but before A closes.
            refreshed = request('POST', base + '/actions/uploadFileMeta',
                params={'shouldCreateRecursively': 'false', 'shouldGenerateNewPar': 'true'},
                headers=upload_headers, json={'action': 'REFRESH'})
            refresh_url = refreshed.json().get('parUrl') if refreshed.ok else None
            if not refresh_url: raise RuntimeError('interleaving refresh failed')
            parsed = urlsplit(refresh_url)
            if parsed.scheme != 'https' or parsed.hostname != 'objectstorage.' + a.region + '.oraclecloud.com' or parsed.username or parsed.password or parsed.port:
                raise RuntimeError('unexpected interleaving PAR origin')
            with requests.Session() as par_client:
                par_client.trust_env = False
                a_result = par_client.put(refresh_url, data=b'first-client', headers={'if-match': put_etag}, timeout=20, allow_redirects=False)
            if a_result.status_code != 200 or not a_result.headers.get('etag'): raise RuntimeError('interleaving first PUT failed')
            other = request('PUT', contents(upload_path), json={'type': 'file', 'format': 'text', 'content': 'second-client-wins'})
            if not other.ok: raise RuntimeError('interleaving second PUT failed')
            stale_close = request('POST', base + '/actions/uploadFileMeta', headers=upload_headers,
                params={'shouldCreateRecursively': 'false'}, json={'action': 'UPDATE', 'eTag': a_result.headers['etag'], 'size': len(b'first-client')})
            check = request('GET', contents(upload_path), params={'content': 1})
            metadata = request('POST', base + '/actions/downloadFileMeta', params={'shouldGenerateNewPar': 'true'}, headers=upload_headers)
            observed = None
            if check.ok:
                model = check.json()
                observed = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
            print('stale metadata close status', stale_close.status_code, 'second client bytes preserved', observed == b'second-client-wins',
                  'metadata size matches bytes', metadata.ok and observed is not None and metadata.json().get('size') == len(observed), flush=True)
            reset = request('PUT', base + '/objects/' + quote(upload_path, safe=''),
                headers={'Content-Type': 'application/octet-stream'}, data=upload_bytes)
            if reset.status_code not in (200, 201): raise RuntimeError('interleaving restore failed')
            updated_meta = request('POST', base + '/actions/downloadFileMeta', params={'shouldGenerateNewPar': 'true'}, headers=upload_headers)
            if not updated_meta.ok or not updated_meta.json().get('eTag'): raise RuntimeError('cannot read restored ETag')
            put_etag = updated_meta.json()['eTag']
        # Probe duplicate create and a transfer interrupted immediately after CREATE.
        if created.status_code in (200, 201):
            duplicate = request('POST', base + '/actions/uploadFileMeta',
                params={'isOverwrite': 'false', 'shouldCreateRecursively': 'false', 'shouldGenerateNewPar': 'true'},
                headers=upload_headers, json={'action': 'CREATE'})
            verify = request('GET', contents(upload_path), params={'content': 1})
            if verify.ok:
                model = verify.json()
                restored = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
                print('upload duplicate create status', duplicate.status_code, 'previous data preserved', restored == upload_bytes, flush=True)
        # REFRESH may obtain an upload PAR without replacing the existing file.
        if created.status_code in (200, 201) and put_etag:
            current_meta = request('POST', base + '/actions/downloadFileMeta',
                params={'shouldGenerateNewPar': 'true'}, headers=upload_headers)
            current_tag = current_meta.json().get('eTag') if current_meta.ok else None
            print('download metadata eTag matches storage PUT eTag', bool(current_tag) and current_tag.strip('"') == put_etag.strip('"'), flush=True)
            refreshed = request('POST', base + '/actions/uploadFileMeta',
                params={'isOverwrite': 'true', 'shouldCreateRecursively': 'false', 'shouldGenerateNewPar': 'true'},
                headers=upload_headers, json={'action': 'REFRESH'})
            refresh_url = refreshed.json().get('parUrl') if refreshed.ok else None
            check = request('GET', contents(upload_path), params={'content': 1})
            prior = None
            if check.ok:
                model = check.json()
                prior = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
            print('REFRESH status', refreshed.status_code, 'PAR present', bool(refresh_url), 'old bytes preserved', prior == upload_bytes, flush=True)
            if refresh_url and prior == upload_bytes:
                parsed = urlsplit(refresh_url)
                if parsed.scheme != 'https' or parsed.hostname != 'objectstorage.' + a.region + '.oraclecloud.com' or parsed.username or parsed.password or parsed.port:
                    raise RuntimeError('unexpected REFRESH PAR origin')
                try:
                    with requests.Session() as par_client:
                        par_client.trust_env = False
                        bad = par_client.put(refresh_url, data=b'wrong', headers={'if-match': '"aidpfs-wrong-etag"'}, timeout=20, allow_redirects=False)
                        valid = par_client.put(refresh_url, data=b'refresh-write', headers={'if-match': put_etag}, timeout=20, allow_redirects=False)
                        stale = par_client.put(refresh_url, data=b'stale', headers={'if-match': put_etag}, timeout=20, allow_redirects=False)
                        print('REFRESH PAR wrong/valid/stale status', bad.status_code, valid.status_code, stale.status_code, flush=True)
                        if valid.status_code in (200, 201, 204) and valid.headers.get('etag') and stale.status_code not in (200, 201, 204):
                            closed = request('POST', base + '/actions/uploadFileMeta', headers=upload_headers,
                                params={'shouldCreateRecursively': 'false'}, json={'action': 'UPDATE', 'eTag': valid.headers['etag'], 'size': len(b'refresh-write')})
                            print('REFRESH close status', closed.status_code, flush=True)
                    check = request('GET', contents(upload_path), params={'content': 1})
                    if check.ok:
                        model = check.json()
                        actual = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
                        print('REFRESH final new bytes', actual == b'refresh-write', flush=True)
                except requests.RequestException as exc:
                    print('REFRESH PAR transfer failed', type(exc).__name__, flush=True)
                # Restore only our own synthetic file before the overwrite probe.
                reset_url = base + '/objects/' + quote(upload_path, safe='')
                reset = request('PUT', reset_url, headers={'Content-Type': 'application/octet-stream'}, data=upload_bytes)
                if reset.status_code not in (200, 201): raise RuntimeError('failed to restore upload fixture')
        # Test conditional writes via Object Storage PAR against this run's
        # existing file, not against any pre-existing workspace data.
        if created.status_code in (200, 201) and put_etag:
            overwrite = request('POST', base + '/actions/uploadFileMeta',
                params={'isOverwrite': 'true', 'shouldCreateRecursively': 'false', 'shouldGenerateNewPar': 'true'},
                headers=upload_headers, json={'action': 'CREATE'})
            before_overwrite = request('GET', contents(upload_path), params={'content': 1})
            if before_overwrite.ok:
                m = before_overwrite.json()
                before_bytes = base64.b64decode(m['content']) if m.get('format') == 'base64' else m['content'].encode()
                print('overwrite PAR creation preserves old data', before_bytes == upload_bytes, 'observed size', len(before_bytes), flush=True)
            overwrite_url = overwrite.json().get('parUrl') if overwrite.ok else None
            print('overwrite PAR creation status', overwrite.status_code, 'PAR present', bool(overwrite_url), flush=True)
            if overwrite_url:
                parsed = urlsplit(overwrite_url)
                if parsed.scheme != 'https' or parsed.hostname != 'objectstorage.' + a.region + '.oraclecloud.com' or parsed.username or parsed.password or parsed.port:
                    raise RuntimeError('unexpected overwrite PAR origin')
                try:
                    with requests.Session() as par_client:
                        par_client.trust_env = False
                        denied = par_client.put(overwrite_url, data=b'bad-condition', headers={'if-match': '"aidpfs-wrong-etag"'}, timeout=20, allow_redirects=False)
                        print('PAR wrong if-match status', denied.status_code, flush=True)
                        valid = par_client.put(overwrite_url, data=b'conditional-update', headers={'if-match': put_etag}, timeout=20, allow_redirects=False)
                        print('PAR original if-match status', valid.status_code, flush=True)
                        stale = par_client.put(overwrite_url, data=b'stale-write', headers={'if-match': put_etag}, timeout=20, allow_redirects=False)
                        print('PAR reused if-match status', stale.status_code, flush=True)
                        if valid.status_code in (200, 201, 204) and valid.headers.get('etag') and stale.status_code not in (200, 201, 204):
                            closed = request('POST', base + '/actions/uploadFileMeta', headers=upload_headers,
                                params={'shouldCreateRecursively': 'false'}, json={'action': 'UPDATE', 'eTag': valid.headers['etag'], 'size': len(b'conditional-update')})
                            print('conditional upload close status', closed.status_code, flush=True)
                    verify = request('GET', contents(upload_path), params={'content': 1})
                    if verify.ok:
                        model = verify.json()
                        final_bytes = base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
                        print('PAR conditional final expected data', final_bytes == b'conditional-update', 'original data', final_bytes == upload_bytes, flush=True)
                except requests.RequestException as exc:
                    print('conditional PAR transfer failed', type(exc).__name__, flush=True)
        interrupted_path = root + '/upload-interrupted.bin'
        interrupted = request('POST', base + '/actions/uploadFileMeta',
            params={'isOverwrite': 'false', 'shouldCreateRecursively': 'false', 'shouldGenerateNewPar': 'true'},
            headers={'path': interrupted_path, 'type': 'FILE'}, json={'action': 'CREATE'})
        if interrupted.status_code in (200, 201):
            owned.add(interrupted_path)
            present = any(x.get('path') == interrupted_path for x in listing(root))
            print('interrupted upload leaves listed object', present, flush=True)
        # Compare WorkspaceObject PUT behavior with the data-plane eTag
        # returned by downloadFileMeta; all payloads remain inside this run's file.
        if par_response.ok and par_response.json().get('eTag'):
            candidate = par_response.json()['eTag']
            def read_file_bytes():
                response = request('GET', contents(file_path), params={'content': 1})
                response.raise_for_status()
                model = response.json()
                return base64.b64decode(model['content']) if model.get('format') == 'base64' else model['content'].encode()
            bad_object = request('PUT', object_url, headers={'if-match': '"aidpfs-obviously-wrong-etag"', 'Content-Type': 'application/octet-stream'}, data=b'bad-if-match')
            after_bad = read_file_bytes()
            print('object PUT wrong if-match status', bad_object.status_code, 'original preserved', after_bad == data, flush=True)
            good_object = request('PUT', object_url, headers={'if-match': candidate, 'Content-Type': 'application/octet-stream'}, data=b'valid-etag-write')
            after_good = read_file_bytes()
            print('object PUT PAR eTag status', good_object.status_code, 'new bytes visible', after_good == b'valid-etag-write', flush=True)
            stale_object = request('PUT', object_url, headers={'if-match': candidate, 'Content-Type': 'application/octet-stream'}, data=b'stale-etag-write')
            after_stale = read_file_bytes()
            print('object PUT reused eTag status', stale_object.status_code, 'previous bytes preserved', after_stale == after_good, flush=True)
            if after_stale != data:
                reset = request('PUT', object_url, headers={'Content-Type': 'application/octet-stream'}, data=data)
                if reset.status_code not in (200, 201) or read_file_bytes() != data: raise RuntimeError('failed to restore probe data')
        if a.mount_binary:
            mount_dir = tempfile.mkdtemp(prefix='aidpfs-write-probe-')
            mount_process = subprocess.Popen([a.mount_binary, '--profile', a.profile, '--region', a.region,
                '--endpoint', a.endpoint, '--platform-id', a.platform_id, '--workspace-key', a.workspace_key,
                '--remote-path', root, '--mount-point', mount_dir], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            for _ in range(200):
                if os.path.ismount(mount_dir) or mount_process.poll() is not None: break
                time.sleep(0.1)
            if not os.path.ismount(mount_dir): raise RuntimeError('probe mount failed')
            mounted_path = os.path.join(mount_dir, 'binary.bin')
            open_handle = open(mounted_path, 'rb', buffering=0)
            before = open_handle.read()
            print('mounted binary SHA256 match', hashlib.sha256(before).digest() == hashlib.sha256(data).digest(),
                  'stat size match', os.stat(mounted_path).st_size == len(data), flush=True)
        # Invalid conditional update must not change data; never assume success means protection.
        r = request('PUT', contents(file_path), headers={'if-match': '"aidpfs-obviously-wrong-etag"'}, json={'type': 'file', 'format': 'text', 'content': 'changed'})
        r2 = request('GET', contents(file_path), params={'content': 1})
        if r2.ok:
            obj = r2.json(); got = base64.b64decode(obj['content']) if obj.get('format') == 'base64' else obj.get('content', '').encode()
            print('wrong if-match rejected', r.status_code in (409, 412), 'original preserved', got == data, flush=True)
            if open_handle:
                open_handle.seek(0)
                old = open_handle.read()
                with open(mounted_path, 'rb') as new_handle:
                    fresh = new_handle.read()
                print('mounted existing handle keeps old bytes', old == data, 'reopen sees updated bytes', fresh == got,
                      'new stat size match', os.stat(mounted_path).st_size == len(got), flush=True)
        notebook = root + '/test.ipynb'
        payload = {'type': 'notebook', 'format': 'json', 'content': {'cells': [], 'metadata': {}, 'nbformat': 4, 'nbformat_minor': 5}}
        r = request('PUT', contents(notebook), json=payload)
        print('notebook direct PUT succeeded', r.status_code in (200, 201), 'ETag present', bool(r.headers.get('etag')), flush=True)
        if r.status_code in (200, 201):
            owned.add(notebook)
            r = request('GET', contents(notebook), params={'content': 1})
            print('notebook content structurally equal', r.ok and r.json().get('content') == payload['content'], 'ETag present', bool(r.headers.get('etag')), flush=True)
            replacement = {'type': 'notebook', 'format': 'json', 'content': {'cells': [], 'metadata': {'aidpfs_probe': True}, 'nbformat': 4, 'nbformat_minor': 5}}
            bad = request('PUT', contents(notebook), headers={'if-match': '"aidpfs-obviously-wrong-etag"'}, json=replacement)
            verify = request('GET', contents(notebook), params={'content': 1})
            print('notebook wrong if-match rejected', bad.status_code in (409, 412), 'original preserved', verify.ok and verify.json().get('content') == payload['content'], flush=True)
        # Exercise representative notebook structure with synthetic cells and output.
        if notebook in owned:
            model = {'cells': [
                {'cell_type': 'markdown', 'id': 'mdprobe1', 'metadata': {'tags': ['probe']}, 'source': ['# synthetic\n']},
                {'cell_type': 'code', 'id': 'codeprobe1', 'metadata': {'custom': 'value'}, 'execution_count': 1,
                 'source': ['print(1)\n'], 'outputs': [{'output_type': 'stream', 'name': 'stdout', 'text': ['1\n']}]}
            ], 'metadata': {'kernelspec': {'name': 'python3', 'display_name': 'Python 3', 'language': 'python'}, 'probe': {'version': 1}},
                'nbformat': 4, 'nbformat_minor': 5}
            rich = request('PUT', contents(notebook), json={'type': 'notebook', 'format': 'json', 'content': model})
            rich_get = request('GET', contents(notebook), params={'content': 1})
            returned = rich_get.json().get('content') if rich_get.ok else None
            print('rich notebook PUT/GET status', rich.status_code, rich_get.status_code,
                  'cells equal', returned is not None and returned.get('cells') == model['cells'],
                  'metadata equal', returned is not None and returned.get('metadata') == model['metadata'],
                  'full model equal', returned == model, flush=True)
            if returned is not None:
                cells = returned.get('cells', [])
                if len(cells) == len(model['cells']):
                    for i, (actual, expected) in enumerate(zip(cells, model['cells'])):
                        keys = sorted(set(actual) | set(expected))
                        print('notebook cell', i, 'differing field names', [k for k in keys if actual.get(k) != expected.get(k)],
                              'source text equivalent', ''.join(actual.get('source', [])) == ''.join(expected.get('source', [])),
                              'output equal', actual.get('outputs') == expected.get('outputs'), flush=True)
                        if i == 1:
                            actual_outputs = actual.get('outputs', [])
                            expected_outputs = expected.get('outputs', [])
                            print('code cell metadata key names', sorted(actual.get('metadata', {}).keys()),
                                  'outputs counts', len(actual_outputs), len(expected_outputs),
                                  'first output type matches', bool(actual_outputs and expected_outputs and actual_outputs[0].get('output_type') == expected_outputs[0].get('output_type')),
                                  'first output text equivalent', bool(actual_outputs and expected_outputs and ''.join(actual_outputs[0].get('text', [])) == ''.join(expected_outputs[0].get('text', []))), flush=True)
                again = request('PUT', contents(notebook), json={'type': 'notebook', 'format': 'json', 'content': returned})
                final = request('GET', contents(notebook), params={'content': 1})
                print('notebook read-save-read status', again.status_code, final.status_code,
                      'roundtrip equal', final.ok and final.json().get('content') == returned,
                      'name matches', final.ok and final.json().get('name') == 'test.ipynb',
                      'size present', final.ok and final.json().get('size') is not None, flush=True)
            notebook_par = request('POST', base + '/actions/downloadFileMeta', params={'shouldGenerateNewPar': 'true'},
                                   headers={'path': notebook, 'type': 'NOTEBOOK'})
            print('notebook download PAR status', notebook_par.status_code,
                  'PAR present', notebook_par.ok and bool(notebook_par.json().get('parUrl')), flush=True)
            notebook_par_url = notebook_par.json().get('parUrl') if notebook_par.ok else None
            if notebook_par_url:
                parsed = urlsplit(notebook_par_url)
                if parsed.scheme != 'https' or parsed.hostname != 'objectstorage.' + a.region + '.oraclecloud.com' or parsed.username or parsed.password or parsed.port:
                    raise RuntimeError('unexpected notebook PAR origin')
                try:
                    with requests.Session() as par_client:
                        par_client.trust_env = False
                        notebook_raw = par_client.get(notebook_par_url, timeout=20, allow_redirects=False)
                        try: parsed_notebook = notebook_raw.json()
                        except ValueError: parsed_notebook = None
                        print('notebook PAR GET status', notebook_raw.status_code,
                              'auth absent', 'Authorization' not in notebook_raw.request.headers,
                              'PAR JSON equals contents', parsed_notebook == returned,
                              'PAR bytes match metadata size', len(notebook_raw.content) == notebook_par.json().get('size'), flush=True)
                except requests.RequestException as exc:
                    print('notebook PAR transfer failed', type(exc).__name__, flush=True)
        # Special names are sent in encoded URL path segments rather than HTTP headers.
        special = root + '/日本 語%#.txt'
        special_create = request('PUT', contents(special), json={'type': 'file', 'format': 'text', 'content': 'synthetic'})
        if special_create.status_code in (200, 201):
            owned.add(special)
            check = request('GET', contents(special), params={'content': 1})
            matches = [x for x in listing(root) if x.get('path') == special]
            print('special path GET', check.status_code, 'name matches', check.ok and check.json().get('name') == '日本 語%#.txt',
                  'bytes match', check.ok and check.json().get('content') == 'synthetic', 'listed exactly once', len(matches) == 1, flush=True)
        print('special path create status', special_create.status_code, flush=True)
        for label, name in [('ascii', 'simple-probe.txt'), ('unicode', '日本語.txt'), ('space', 'space name.txt'), ('percent', 'percent%name.txt'), ('hash', 'hash#name.txt')]:
            special_item = root + '/' + name
            created_item = request('PUT', contents(special_item), json={'type': 'file', 'format': 'text', 'content': 'synthetic'})
            if created_item.status_code in (200, 201):
                owned.add(special_item)
                read_item = request('GET', contents(special_item), params={'content': 1})
                print('path case', label, 'create', created_item.status_code, 'GET', read_item.status_code,
                      'name equal', read_item.ok and read_item.json().get('name') == name, flush=True)
            else:
                try: code = created_item.json().get('code')
                except ValueError: code = None
                print('path case', label, 'create', created_item.status_code, 'error code', code, flush=True)
        # Rename only generated files; retain both candidate names in the
        # journalled folder if a request result is ambiguous.
        renamed = root + '/renamed.bin'
        rename = request('PATCH', contents(file_path), headers={'if-match': '"aidpfs-obviously-wrong-etag"'}, json={'path': renamed.lstrip('/')})
        if rename.status_code in (200, 201): owned.add(renamed)
        current = {entry.get('path') for entry in listing(root)}
        print('rename with wrong if-match status', rename.status_code, 'old present', file_path in current,
              'new present', renamed in current, flush=True)
        if notebook in owned and renamed in owned:
            collision = request('PATCH', contents(notebook), json={'path': renamed.lstrip('/')})
            current = {entry.get('path') for entry in listing(root)}
            print('rename to existing destination status', collision.status_code,
                  'source present', notebook in current, 'destination present', renamed in current, flush=True)
        print('probe finished', flush=True)
    finally:
        if open_handle:
            open_handle.close()
        if mount_dir:
            if os.path.ismount(mount_dir):
                result = subprocess.run(['fusermount3', '-u', mount_dir], capture_output=True, timeout=15)
                print('probe unmount status', result.returncode, flush=True)
            if mount_process and mount_process.poll() is None:
                mount_process.send_signal(signal.SIGTERM)
                try: mount_process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    mount_process.kill()
                    mount_process.wait()
            if os.path.ismount(mount_dir): raise RuntimeError('mount still active; journal retained')
            os.rmdir(mount_dir)
        if created_root:
            try:
                if nested_pending: raise RuntimeError('nested probe outcome requires manual inspection')
                children = listing(root)
                unknown = [x for x in children if x.get('path') not in owned]
                if unknown:
                    print('cleanup stopped: unexpected entries', len(unknown), flush=True)
                    raise RuntimeError('unexpected entries')
                else:
                    for x in children:
                        if (x['path'] == root + '/nonempty-test' or x.get('type') in ('FOLDER', 'DIRECTORY')) and listing(x['path']):
                            raise RuntimeError('refusing to delete nonempty folder')
                        r = request('DELETE', contents(x['path']))
                        if r.status_code not in (200, 204, 404): raise RuntimeError('child deletion failed')
                    if listing(root): raise RuntimeError('folder not empty after cleanup')
                    r = request('DELETE', contents(root))
                    if r.status_code not in (200, 204): raise RuntimeError('folder deletion failed')
                    remains = any(x.get('path') == root for x in listing('/Workspace/Shared'))
                    print('cleanup verified', not remains, flush=True)
                    if remains: raise RuntimeError('probe root remains after deletion')
                    journal.unlink()
            except (Exception, KeyboardInterrupt) as e:
                print('cleanup incomplete:', type(e).__name__, 'journal retained', flush=True)
                raise RuntimeError('cleanup incomplete; inspect the private journal') from e
        else:
            print('root ownership not confirmed: journal retained', flush=True)

if __name__ == '__main__':
    main()
