#!/usr/bin/env python3
"""Isolated AIDP write probe. Requires OCI Python SDK, requests and explicit IDs.

Only creates a randomly named directory under /Workspace/Shared. An interrupted
run leaves a mode-0600 journal; cleanup never recursively removes unknown entries.
"""
import argparse
import json
import hashlib
import os
import pathlib
import signal
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
    p.add_argument('--region', default='ap-osaka-1')
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
    try:
        if any(x.get('path') == root for x in listing('/Workspace/Shared')):
            raise RuntimeError('random probe root already exists')
        r = request('POST', base + '/objects', headers={'path': root, 'type': 'FOLDER', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=b'')
        if r.status_code not in (200, 201):
            raise RuntimeError('probe root creation failed or ambiguous; inspect journal')
        created_root = True
        print('root created; empty listing:', len(listing(root)) == 0, flush=True)
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
            import base64
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
                children = listing(root)
                unknown = [x for x in children if x.get('path') not in owned]
                if unknown:
                    print('cleanup stopped: unexpected entries', len(unknown), flush=True)
                    raise RuntimeError('unexpected entries')
                else:
                    for x in children:
                        r = request('DELETE', contents(x['path']))
                        if r.status_code not in (200, 204, 404): raise RuntimeError('child deletion failed')
                    if listing(root): raise RuntimeError('folder not empty after cleanup')
                    r = request('DELETE', contents(root))
                    if r.status_code not in (200, 204): raise RuntimeError('folder deletion failed')
                    remains = any(x.get('path') == root for x in listing('/Workspace/Shared'))
                    print('cleanup verified', not remains, flush=True)
                    if not remains: journal.unlink()
            except (Exception, KeyboardInterrupt) as e:
                print('cleanup incomplete:', type(e).__name__, 'journal retained', flush=True)
                raise RuntimeError('cleanup incomplete; inspect the private journal') from e
        else:
            print('root ownership not confirmed: journal retained', flush=True)

if __name__ == '__main__':
    main()
