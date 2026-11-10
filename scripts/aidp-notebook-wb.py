#!/usr/bin/env python3
"""Stateful helper for manual Workbench vs PAR Notebook checks (TASK section 11).

Subcommands share a private state file (--state, mode 0600):
  setup             create a random root and a synthetic rich notebook (via PAR)
  state             print storage ETag, size, sha256 and markers (no content)
  par-write MARK    conditional PAR write of the fixture with metadata marker MARK,
                    using the ETag recorded by the last `state` (prints 412 on conflict)
  diff              compare current stored notebook with the original fixture
  cleanup           delete the notebook and the empty root, then the state file
Reuses the probe helpers; never prints PAR URLs, IDs or notebook bodies.
"""
import argparse
import hashlib
import json
import os
import sys
import uuid
from urllib.parse import quote

import importlib.util

here = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location('nbprobe', os.path.join(here, 'aidp-notebook-probe.py'))
nbprobe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(nbprobe)

import oci
import requests
from urllib.parse import urlsplit

GO_PATH_SAFE = nbprobe.GO_PATH_SAFE


def connect(profile, endpoint):
    c = oci.config.from_file(profile_name=profile)
    s = requests.Session()
    s.auth = oci.signer.Signer(tenancy=c['tenancy'], user=c['user'], fingerprint=c['fingerprint'],
                               private_key_file_location=c['key_file'], pass_phrase=c.get('pass_phrase'))
    idc = oci.identity.IdentityClient(c)
    comps = [c['tenancy']] + [x.id for x in oci.pagination.list_call_get_all_results(
        idc.list_compartments, c['tenancy'], compartment_id_in_subtree=True, lifecycle_state='ACTIVE').data]
    pid = [p['id'] for comp in comps for p in (s.get(endpoint + '/20240831/aiDataPlatforms', params={'compartmentId': comp}, timeout=20).json().get('items', []))
           if p['displayName'] == 'aidp-sandbox' and p['lifecycleState'] == 'ACTIVE']
    if len(pid) != 1:
        sys.exit('platform not uniquely found')
    pb = endpoint + '/20260430/aiDataPlatforms/' + quote(pid[0], safe='')
    w = [x for x in s.get(pb + '/workspaces', timeout=20).json()['items'] if 'sandbox' in (x.get('displayName'), x.get('name'))]
    return s, pb + '/workspaces/' + quote(w[0].get('key') or w[0].get('workspaceKey') or w[0].get('id'), safe='')


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--state', required=True)
    p.add_argument('--profile', default='OCI_TOKYO')
    p.add_argument('--endpoint', default='https://aidp.ap-osaka-1.oci.oraclecloud.com')
    p.add_argument('--region', default='ap-osaka-1')
    p.add_argument('cmd', choices=['setup', 'state', 'par-write', 'diff', 'cleanup'])
    p.add_argument('mark', nargs='?')
    a = p.parse_args()
    s, base = connect(a.profile, a.endpoint)
    par = requests.Session()
    par_host = f'objectstorage.{a.region}.oraclecloud.com'

    if a.cmd == 'setup':
        st = {'root': '/Workspace/Shared/aidpfs-wb-probe-' + uuid.uuid4().hex}
        st['nb'] = st['root'] + '/AidpfsWorkbenchProbe.ipynb'
        fd = os.open(a.state, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        os.close(fd)
    else:
        st = json.load(open(a.state))
    root, nb = st['root'], st['nb']

    def curl(path):
        assert path == root or path.startswith(root + '/')
        return base + '/notebook/api/contents/' + quote(path.lstrip('/'), safe=GO_PATH_SAFE)

    def meta(action, etag=None, size=None):
        h = {'path': nb, 'type': 'NOTEBOOK', 'should-update-recent': 'false', 'Content-Type': 'application/json'}
        if action == 'DOWNLOAD':
            r = s.post(base + '/actions/downloadFileMeta', params={'shouldGenerateNewPar': 'true'}, headers=h, data=b'', timeout=60)
        else:
            body, q = {'action': action}, {'shouldCreateRecursively': 'false', 'shouldGenerateNewPar': 'true'}
            if action == 'UPDATE':
                body.update(eTag=etag, size=size)
                del q['shouldGenerateNewPar']
            r = s.post(base + '/actions/uploadFileMeta', params=q, headers=h, data=json.dumps(body), timeout=60)
        print(f'  {action}: {r.status_code} rid={r.headers.get("opc-request-id")}')
        r.raise_for_status()
        j = r.json()
        if j.get('parUrl'):
            u = urlsplit(j['parUrl'])
            assert u.scheme == 'https' and u.hostname == par_host
        return j

    def current():
        m = meta('DOWNLOAD')
        r = par.get(m['parUrl'], timeout=60)
        r.raise_for_status()
        return r.content, m.get('eTag'), m.get('size')

    def save(st):
        with open(a.state, 'w') as f:
            json.dump(st, f)

    def par_write(data, base_etag):
        up = meta('REFRESH')
        r = par.put(up['parUrl'], data=data, headers={'If-Match': base_etag, 'Content-Type': 'application/octet-stream'}, timeout=60)
        print(f'  PAR PUT: {r.status_code}')
        if r.status_code == 412:
            return None
        r.raise_for_status()
        meta('UPDATE', r.headers['ETag'], len(data))
        return r.headers['ETag']

    def summarize(data):
        nbj = json.loads(data)
        cells = nbj.get('cells', [])
        return {'sha256': hashlib.sha256(data).hexdigest()[:16], 'bytes': len(data),
                'marker': nbj.get('metadata', {}).get('aidpfs_marker'), 'cells': len(cells),
                'cell_ids': [c.get('id') for c in cells],
                'first_md_source': ''.join(next((c['source'] for c in cells if c.get('cell_type') == 'markdown'), ''))[:60]}

    if a.cmd == 'setup':
        fixture = nbprobe.fixtures()['rich']
        fixture['metadata']['aidpfs_marker'] = 'initial'
        r = s.post(base + '/objects', headers={'path': root, 'type': 'FOLDER', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=b'', timeout=60)
        print('  root create:', r.status_code)
        r.raise_for_status()
        r = s.put(curl(nb), headers={'Content-Type': 'application/json'}, data=json.dumps({'type': 'notebook', 'format': 'json', 'content': {'cells': [], 'metadata': {}, 'nbformat': 4, 'nbformat_minor': 5}}), timeout=60)
        print('  notebook create:', r.status_code)
        r.raise_for_status()
        _, etag, _ = current()
        raw = nbprobe.serialize(fixture)
        new = par_write(raw, etag)
        st.update(fixture=fixture, last_etag=new)
        save(st)
        print('root', root, 'notebook', nb.rsplit('/', 1)[1])
        print(json.dumps(summarize(raw)))
    elif a.cmd == 'state':
        data, etag, size = current()
        st['last_etag'] = etag
        save(st)
        print(json.dumps(dict(summarize(data), etag_changed_since_setup=etag != st.get('setup_etag', etag), meta_size=size)))
    elif a.cmd == 'par-write':
        nbj = json.loads(nbprobe.serialize(st['fixture']))
        nbj['metadata']['aidpfs_marker'] = a.mark
        new = par_write(nbprobe.serialize(nbj), st['last_etag'])
        print('result:', 'conflict 412' if new is None else 'written')
        if new:
            st['last_etag'] = new
            save(st)
    elif a.cmd == 'diff':
        data, _, _ = current()
        got = json.loads(data)
        exp = st['fixture']
        d = nbprobe.diff(nbprobe.normalize(exp), nbprobe.normalize(got))
        print('exact bytes equal to fixture serialization:', data == nbprobe.serialize(exp))
        print('stored formatting is indent=1 sorted:', data == nbprobe.serialize(got))
        print(f'{len(d)} normalized diffs:')
        for x in d[:40]:
            print('   ', x)
    elif a.cmd == 'cleanup':
        lst = s.get(base + '/objects', params={'path': root, 'limit': 1000}, timeout=30).json()['items']
        for it in lst:
            if it['path'] != nb:
                sys.exit('unexpected entry in root; inspect manually: ' + it['path'])
        if lst:
            print('  delete notebook:', s.delete(curl(nb), timeout=60).status_code)
        print('  delete root:', s.delete(curl(root), timeout=60).status_code)
        gone = root not in [x['path'] for x in s.get(base + '/objects', params={'path': '/Workspace/Shared', 'limit': 1000}, timeout=30).json()['items']]
        print('root gone:', gone)
        if gone:
            os.unlink(a.state)


if __name__ == '__main__':
    main()
