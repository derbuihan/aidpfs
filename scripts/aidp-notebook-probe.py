#!/usr/bin/env python3
"""Isolated AIDP Notebook representation probe (TASK section 11).

Creates one random root under /Workspace/Shared holding synthetic notebooks
only, writes them through the conditional PAR route and through contents PUT,
and compares what PAR raw bytes, contents GET and export return. No code is
executed. PAR URLs are never printed. Cleanup deletes only entries under the
fresh root, leaves first; it stops when an outcome is unknown.

  uv run --no-project --with oci --with requests python -I scripts/aidp-notebook-probe.py --journal /tmp/x.json
"""
import argparse
import base64
import copy
import hashlib
import json
import os
import pathlib
import sys
import time
import uuid
from urllib.parse import quote, urlsplit

import oci
import requests

GO_PATH_SAFE = "$&+,;=:@-_.~"
PNG_1PX = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='


class Unknown(Exception):
    pass


def fixtures():
    base_meta = {'kernelspec': {'display_name': 'Python 3', 'language': 'python', 'name': 'python3'},
                 'language_info': {'name': 'python', 'version': '3.11.0', 'mimetype': 'text/x-python',
                                   'file_extension': '.py', 'codemirror_mode': {'name': 'ipython', 'version': 3}}}
    empty = {'cells': [], 'metadata': {}, 'nbformat': 4, 'nbformat_minor': 5}
    markdown = {'cells': [{'cell_type': 'markdown', 'id': 'md-0001', 'metadata': {'aidpfs_cell': 'md'},
                           'source': ['# 見出し aidpfs\n', '\n', 'text with ![img](attachment:a.png)'],
                           'attachments': {'a.png': {'image/png': PNG_1PX}}}],
                'metadata': {}, 'nbformat': 4, 'nbformat_minor': 5}
    code = {'cells': [{'cell_type': 'code', 'id': 'code-0001', 'execution_count': 3,
                       'metadata': {'tags': ['aidpfs'], 'collapsed': False, 'aidpfs_unknown': {'nested': [1, 2, {'k': None}]}},
                       'source': 'print("aidpfs")\nx = 1',
                       'outputs': [
                           {'output_type': 'stream', 'name': 'stdout', 'text': ['aidpfs\n']},
                           {'output_type': 'execute_result', 'execution_count': 3, 'metadata': {},
                            'data': {'text/plain': ['1'], 'image/png': PNG_1PX}},
                           {'output_type': 'display_data', 'metadata': {'image/png': {'width': 1}},
                            'data': {'text/html': ['<b>aidpfs</b>'], 'text/plain': ['<IPython.core.display.HTML object>']}},
                           {'output_type': 'error', 'ename': 'ValueError', 'evalue': 'aidpfs synthetic',
                            'traceback': ['Traceback (most recent call last)', 'ValueError: aidpfs synthetic']}]}],
            'metadata': base_meta, 'nbformat': 4, 'nbformat_minor': 5}
    mixed = {'cells': [
        {'cell_type': 'markdown', 'id': 'mix-1', 'metadata': {}, 'source': 'intro'},
        {'cell_type': 'code', 'id': 'mix-2', 'execution_count': None, 'metadata': {}, 'source': ['a = 1\n', 'b = 2'], 'outputs': []},
        {'cell_type': 'raw', 'id': 'mix-3', 'metadata': {'format': 'text/plain'}, 'source': 'raw text'},
        {'cell_type': 'code', 'id': 'mix-4', 'execution_count': 1, 'metadata': {}, 'source': '', 'outputs': []}],
        'metadata': base_meta, 'nbformat': 4, 'nbformat_minor': 5}
    rich = copy.deepcopy(code)
    rich['cells'] = mixed['cells'] + code['cells'] + markdown['cells']
    rich['metadata'] = dict(base_meta, **{
        'widgets': {'application/vnd.jupyter.widget-state+json': {'version_major': 2, 'version_minor': 0, 'state': {
            'w1': {'model_name': 'IntSliderModel', 'model_module': '@jupyter-widgets/controls',
                   'model_module_version': '2.0.0', 'state': {'value': 7}}}}},
        'aidpfs_notebook_unknown': {'a': [True, 1.5, 'ü']}})
    rich['aidpfs_top_level_unknown'] = {'kept': 'maybe'}
    return {'empty': empty, 'markdown': markdown, 'code': code, 'mixed': mixed, 'rich': rich}


def serialize(nb):
    # Jupyter's on-disk format: indent=1, sorted keys, non-ASCII kept, trailing newline.
    return (json.dumps(nb, indent=1, sort_keys=True, ensure_ascii=False) + '\n').encode('utf-8')


def norm_source(v):
    return ''.join(v) if isinstance(v, list) else v


def normalize(nb):
    """nbformat-equivalent form: multiline strings joined."""
    nb = copy.deepcopy(nb)
    for c in nb.get('cells', []):
        if 'source' in c:
            c['source'] = norm_source(c['source'])
        for o in c.get('outputs', []):
            if 'text' in o:
                o['text'] = norm_source(o['text'])
            for k, v in list(o.get('data', {}).items()):
                o['data'][k] = norm_source(v)
    return nb


def diff(a, b, path='', out=None):
    out = [] if out is None else out
    if isinstance(a, dict) and isinstance(b, dict):
        for k in sorted(set(a) | set(b)):
            if k not in a:
                out.append(f'+{path}/{k}')
            elif k not in b:
                out.append(f'-{path}/{k}')
            else:
                diff(a[k], b[k], f'{path}/{k}', out)
    elif isinstance(a, list) and isinstance(b, list) and len(a) == len(b):
        for i, (x, y) in enumerate(zip(a, b)):
            diff(x, y, f'{path}[{i}]', out)
    elif a != b:
        out.append(f'~{path}: {json.dumps(a, ensure_ascii=False)[:60]} -> {json.dumps(b, ensure_ascii=False)[:60]}')
    return out


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--profile', default='OCI_TOKYO')
    p.add_argument('--endpoint', default='https://aidp.ap-osaka-1.oci.oraclecloud.com')
    p.add_argument('--region', default='ap-osaka-1')
    p.add_argument('--platform-name', default='aidp-sandbox')
    p.add_argument('--workspace-name', default='sandbox')
    p.add_argument('--journal', required=True)
    p.add_argument('--cases', default='LP', help='L: lossless matrix, P: PAR CAS on notebooks')
    p.add_argument('--keep', action='store_true', help='keep fixtures (for Workbench checks); print root')
    a = p.parse_args()

    c = oci.config.from_file(profile_name=a.profile)
    signer = oci.signer.Signer(tenancy=c['tenancy'], user=c['user'], fingerprint=c['fingerprint'],
                               private_key_file_location=c['key_file'], pass_phrase=c.get('pass_phrase'))
    session = requests.Session()
    session.auth = signer
    par = requests.Session()  # bearer PAR requests are never signed
    par_host = f'objectstorage.{a.region}.oraclecloud.com'

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
    base = pbase + '/workspaces/' + quote(match[0].get('key') or match[0].get('workspaceKey') or match[0].get('id'), safe='')

    root = '/Workspace/Shared/aidpfs-notebook-probe-' + uuid.uuid4().hex
    fd = os.open(a.journal, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as f:
        json.dump({'root': root, 'profile': a.profile}, f)
        f.flush()
        os.fsync(f.fileno())
    unknown, results = [], []

    def log(*x):
        print(*x, flush=True)

    def inside(path):
        if not (path == root or path.startswith(root + '/')):
            raise ValueError('outside probe root')
        return path

    def curl(path):
        return base + '/notebook/api/contents/' + quote(inside(path).lstrip('/'), safe=GO_PATH_SAFE)

    def req(label, method, url, **kw):
        try:
            r = session.request(method, url, timeout=60, allow_redirects=False, **kw)
        except requests.exceptions.RequestException as e:
            log(f'  {label}: {method} EXC {type(e).__name__}')
            raise Unknown(label)
        msg = ''
        if r.status_code >= 400:
            try:
                msg = ' code=%s message=%s' % (r.json().get('code'), str(r.json().get('message'))[:160])
            except ValueError:
                pass
        log(f'  {label}: {method} {r.status_code} rid={r.headers.get("opc-request-id")}{msg}')
        return r

    def listing(path):
        items, page = [], None
        while True:
            q = {'path': path, 'limit': 1000}
            if page:
                q['page'] = page
            r = session.get(base + '/objects', params=q, timeout=30)
            r.raise_for_status()
            items += r.json()['items']
            page = r.headers.get('opc-next-page')
            if not page:
                return {x['path']: x.get('type') for x in items}

    def tree(path):
        out = {}
        for child, kind in listing(path).items():
            out[child] = kind
            if kind == 'FOLDER':
                out.update(tree(child))
        return out

    def record(name, ok, detail=''):
        results.append((name, ok, detail))
        log(f'RESULT {name}: {"OK" if ok else "NOTE"} {detail}')

    def check_par(url):
        u = urlsplit(url)
        if u.scheme != 'https' or u.hostname != par_host:
            raise RuntimeError('unexpected PAR origin')
        return url

    def file_meta(path, action, etag=None, size=None):
        """downloadFileMeta (action=DOWNLOAD) or uploadFileMeta REFRESH/UPDATE for a NOTEBOOK."""
        h = {'path': inside(path), 'type': 'NOTEBOOK', 'should-update-recent': 'false', 'Content-Type': 'application/json'}
        if action == 'DOWNLOAD':
            r = req('downloadFileMeta', 'POST', base + '/actions/downloadFileMeta', params={'shouldGenerateNewPar': 'true'}, headers=h, data=b'')
        else:
            body = {'action': action}
            q = {'shouldCreateRecursively': 'false', 'shouldGenerateNewPar': 'true'}
            if action == 'UPDATE':
                body.update(eTag=etag, size=size)
                del q['shouldGenerateNewPar']
            r = req('uploadFileMeta ' + action, 'POST', base + '/actions/uploadFileMeta', params=q, headers=h, data=json.dumps(body))
        if r.status_code not in (200, 201):
            raise RuntimeError(f'{action} failed {r.status_code}')
        j = r.json()
        return {'url': check_par(j['parUrl']) if j.get('parUrl') else None, 'etag': j.get('eTag'), 'size': j.get('size')}

    def par_get(path):
        m = file_meta(path, 'DOWNLOAD')
        r = par.get(m['url'], timeout=60, allow_redirects=False)
        log(f'  PAR GET {r.status_code} bytes={len(r.content)} meta_size={m["size"]}')
        if r.status_code != 200:
            raise RuntimeError('PAR GET failed')
        return r.content, m['etag'], r.headers.get('ETag')

    def par_write(path, data, base_etag):
        up = file_meta(path, 'REFRESH')
        try:
            r = par.put(up['url'], data=data, headers={'If-Match': base_etag, 'Content-Type': 'application/octet-stream'}, timeout=60, allow_redirects=False)
        except requests.exceptions.RequestException:
            raise Unknown('PAR PUT')
        log(f'  PAR PUT {r.status_code}')
        if r.status_code == 412:
            return 412, None
        if r.status_code not in (200, 201, 204) or not r.headers.get('ETag'):
            raise Unknown('PAR PUT status %d' % r.status_code)
        new = r.headers['ETag']
        file_meta(path, 'UPDATE', new, len(data))
        return r.status_code, new

    def put_notebook(path, nb):
        r = req('contents PUT', 'PUT', curl(path), headers={'Content-Type': 'application/json'},
                data=json.dumps({'type': 'notebook', 'format': 'json', 'content': nb}))
        if r.status_code not in (200, 201):
            raise RuntimeError('contents PUT failed')
        return r.json().get('content')

    def contents_get(path):
        r = req('contents GET', 'GET', curl(path), params={'content': 1})
        return r.json().get('content') if r.ok else None

    def export(path):
        r = req('export', 'POST', base + '/notebook/api/actions/export/contents/' + quote(inside(path).lstrip('/'), safe=GO_PATH_SAFE),
                headers={'Content-Type': 'application/json'}, data=json.dumps({'format': 'ipynb'}))
        if not r.ok:
            return None, None
        j = r.json()
        content = j.get('content')
        shape = type(content).__name__
        if isinstance(content, str):
            try:
                content = json.loads(content)
            except ValueError:
                pass
        return content, shape

    def compare(label, expected, got):
        if got is None:
            record(label, False, 'unavailable')
            return
        exact = got == expected
        d = diff(normalize(expected), normalize(got)) if isinstance(got, dict) else ['not an object']
        record(label, exact or not d, f'exact={exact} normalized_equal={not d} diffs={len(d)} ' + '; '.join(d[:12]))

    try:
        if root in listing('/Workspace/Shared'):
            raise RuntimeError('root exists')
        r = req('root create', 'POST', base + '/objects', headers={'path': root, 'type': 'FOLDER', 'is-overwrite': 'false', 'Content-Type': 'application/octet-stream'}, data=b'')
        if r.status_code not in (200, 201):
            raise RuntimeError('root create failed')

        if 'L' in a.cases:
            for name, nb in fixtures().items():
                log(f'L {name}')
                raw = serialize(nb)
                # Route 1: write raw .ipynb bytes through conditional PAR.
                pth = root + f'/par-{name}.ipynb'
                put_notebook(pth, {'cells': [], 'metadata': {}, 'nbformat': 4, 'nbformat_minor': 5})
                before, meta_etag, _ = par_get(pth)
                status, new_etag = par_write(pth, raw, meta_etag)
                record(f'L {name} PAR write', status in (200, 201, 204), f'status={status} sha={hashlib.sha256(raw).hexdigest()[:12]} bytes={len(raw)}')
                back, _, _ = par_get(pth)
                record(f'L {name} PAR->PAR bytes', back == raw, f'equal={back == raw} len={len(back)}/{len(raw)}')
                compare(f'L {name} PAR->contents GET', nb, contents_get(pth))
                ex, shape = export(pth)
                compare(f'L {name} PAR->export ({shape})', nb, ex)
                # Route 2: contents PUT, then read the stored bytes.
                pth2 = root + f'/put-{name}.ipynb'
                returned = put_notebook(pth2, nb)
                compare(f'L {name} PUT response', nb, returned)
                stored, _, _ = par_get(pth2)
                try:
                    stored_nb = json.loads(stored)
                except ValueError:
                    stored_nb = None
                fmt = 'indent=1 sorted' if stored == serialize(stored_nb or {}) else 'other'
                compare(f'L {name} PUT->PAR stored ({fmt})', nb, stored_nb)
                compare(f'L {name} PUT->contents GET', nb, contents_get(pth2))

        if 'P' in a.cases:
            log('P notebook PAR CAS')
            pth = root + '/cas.ipynb'
            put_notebook(pth, {'cells': [], 'metadata': {'m': 0}, 'nbformat': 4, 'nbformat_minor': 5})
            _, e0, _ = par_get(pth)
            a_nb = {'cells': [], 'metadata': {'m': 'A'}, 'nbformat': 4, 'nbformat_minor': 5}
            b_nb = {'cells': [], 'metadata': {'m': 'B'}, 'nbformat': 4, 'nbformat_minor': 5}
            sa, _ = par_write(pth, serialize(a_nb), e0)
            sb, _ = par_write(pth, serialize(b_nb), e0)
            got = contents_get(pth)
            record('P stale PAR writer', sa in (200, 201, 204) and sb == 412 and got and got.get('metadata', {}).get('m') == 'A',
                   f'first={sa} stale={sb} contents_marker={got and got.get("metadata", {}).get("m")}')
            # A contents PUT does not move the storage ETag check? Observe.
            _, e1, _ = par_get(pth)
            put_notebook(pth, {'cells': [], 'metadata': {'m': 'REST'}, 'nbformat': 4, 'nbformat_minor': 5})
            sc, _ = par_write(pth, serialize({'cells': [], 'metadata': {'m': 'C'}, 'nbformat': 4, 'nbformat_minor': 5}), e1)
            got = contents_get(pth)
            record('P PAR after contents PUT', True, f'par_status={sc} contents_marker={got and got.get("metadata", {}).get("m")}')
    except Unknown as e:
        unknown.append(str(e))
    finally:
        if a.keep and not unknown:
            log('KEEP root for manual checks:', root, 'journal', a.journal)
        elif unknown:
            log('UNKNOWN outcomes, cleanup stopped:', unknown, 'journal kept at', a.journal)
        else:
            try:
                entries = tree(root)
                for pth, kind in sorted(entries.items(), key=lambda x: -x[0].count('/')):
                    if kind != 'FOLDER':
                        if req('cleanup', 'DELETE', curl(pth)).status_code != 204:
                            raise RuntimeError('cleanup failed ' + pth)
                for pth, kind in sorted(entries.items(), key=lambda x: -x[0].count('/')):
                    if kind == 'FOLDER':
                        if listing(pth):
                            raise RuntimeError('folder not empty')
                        req('cleanup', 'DELETE', curl(pth))
                if listing(root):
                    raise RuntimeError('root not empty')
                req('cleanup root', 'DELETE', curl(root))
                if root not in listing('/Workspace/Shared'):
                    log('root gone: True')
                    pathlib.Path(a.journal).unlink()
            except Exception as e:
                log('cleanup error', type(e).__name__, e, 'journal kept at', a.journal)
        log('SUMMARY')
        for name, ok, detail in results:
            log(f'  {"OK  " if ok else "NOTE"} {name}: {detail}')


if __name__ == '__main__':
    main()
