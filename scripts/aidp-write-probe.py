#!/usr/bin/env python3
"""Isolated AIDP write probe. Requires OCI Python SDK, requests and explicit IDs.

Only creates a randomly named directory under /Workspace/Shared. An interrupted
run leaves a mode-0600 journal; cleanup never recursively removes unknown entries.
"""
import argparse
import json
import os
import pathlib
import signal
import sys
import uuid
from urllib.parse import quote

import oci
import requests


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--platform-id', required=True)
    p.add_argument('--workspace-key', required=True)
    p.add_argument('--profile', default='OCI_TOKYO')
    p.add_argument('--endpoint', default='https://aidp.ap-osaka-1.oci.oraclecloud.com')
    p.add_argument('--journal', required=True, help='new private recovery journal path')
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
        # Invalid conditional update must not change data; never assume success means protection.
        r = request('PUT', contents(file_path), headers={'if-match': '"aidpfs-obviously-wrong-etag"'}, json={'type': 'file', 'format': 'text', 'content': 'changed'})
        r2 = request('GET', contents(file_path), params={'content': 1})
        if r2.ok:
            obj = r2.json(); got = base64.b64decode(obj['content']) if obj.get('format') == 'base64' else obj.get('content', '').encode()
            print('wrong if-match rejected', r.status_code in (409, 412), 'original preserved', got == data, flush=True)
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
        print('probe finished', flush=True)
    finally:
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
