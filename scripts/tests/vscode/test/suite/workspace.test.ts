import * as assert from 'assert';
import * as childProcess from 'child_process';
import * as fs from 'fs/promises';
import * as path from 'path';
import * as vscode from 'vscode';

suite('AIDPFS mounted workspace', () => {
  const root = process.env.AIDPFS_E2E_WORKSPACE;
  const rel = process.env.AIDPFS_E2E_FILE;
  if (!root || !rel || rel.includes('/') || rel === '.' || rel === '..') {
    throw new Error('AIDPFS_E2E_WORKSPACE and a basename-only AIDPFS_E2E_FILE are required');
  }
  const file = path.join(root, rel);
  const externalRel = process.env.AIDPFS_E2E_EXTERNAL_FILE;
  const externalFile = externalRel ? path.join(root, externalRel) : '';
  const uri = vscode.Uri.file(file);

  test('open, edit, preserve dirty buffer, and rename/delete a closed FILE', async () => {
    const initial = await fs.readFile(file, 'utf8');
    assert.ok(initial.includes('aidpfs-vscode-seed'));
    const found = await vscode.workspace.findFiles('**/*.txt', '**/.vscode/**');
    assert.ok(found.some((item) => item.fsPath === file), 'VS Code search should find the synthetic FILE');

    const document = await vscode.workspace.openTextDocument(uri);
    await vscode.window.showTextDocument(document);
    assert.strictEqual(document.getText(), initial);

    assert.ok(externalFile, 'external update fixture is required');
    const heldExternalHandle = await fs.open(externalFile, 'r');
    try {
      const heldBytes = Buffer.alloc(512);
      const readHeld = async (): Promise<string> => {
        const result = await heldExternalHandle.read(heldBytes, 0, heldBytes.length, 0);
        return heldBytes.subarray(0, result.bytesRead).toString('utf8');
      };
      assert.ok((await readHeld()).includes('aidpfs-vscode-external-seed'));
      const helper = path.resolve(__dirname, '../../../external-update.py');
      childProcess.execFileSync(process.env.AIDPFS_PYTHON || 'python3', [helper],
        { env: process.env, stdio: 'pipe', timeout: 60_000 });
      assert.ok((await readHeld()).includes('aidpfs-vscode-external-seed'),
        'an open FUSE handle should retain its original snapshot');
    } finally {
      await heldExternalHandle.close();
    }
    assert.strictEqual(await fs.readFile(externalFile, 'utf8'), 'aidpfs-vscode-external-update\n',
      'a new open should observe the external AIDP update');

    const edit = new vscode.WorkspaceEdit();
    edit.insert(uri, new vscode.Position(document.lineCount, 0), 'aidpfs-vscode-saved\n');
    assert.ok(await vscode.workspace.applyEdit(edit));
    assert.strictEqual(document.isDirty, true);
    assert.ok(await document.save());
    assert.strictEqual(document.isDirty, false);
    await vscode.commands.executeCommand('workbench.action.closeActiveEditor');
    const reopened = await vscode.workspace.openTextDocument(uri);
    assert.ok(reopened.getText().includes('aidpfs-vscode-saved'));
    assert.strictEqual(await fs.readFile(file, 'utf8'), reopened.getText());

    const dirtyEdit = new vscode.WorkspaceEdit();
    dirtyEdit.insert(uri, new vscode.Position(reopened.lineCount, 0), 'aidpfs-vscode-dirty\n');
    assert.ok(await vscode.workspace.applyEdit(dirtyEdit));
    assert.strictEqual(reopened.isDirty, true);
    assert.ok(reopened.getText().includes('aidpfs-vscode-dirty'));

    const held = await fs.open(file, 'r');
    try {
      await assert.rejects(() => Promise.resolve(vscode.workspace.fs.rename(uri, vscode.Uri.file(`${file}.renamed`))));
      await assert.rejects(() => Promise.resolve(vscode.workspace.fs.delete(uri)));
    } finally {
      await held.close();
    }
    assert.ok((await fs.readFile(file, 'utf8')).includes('aidpfs-vscode-saved'));
    assert.strictEqual(reopened.isDirty, true);
    reopened.isDirty && await vscode.workspace.fs.writeFile(uri, Buffer.from(reopened.getText()));
    await reopened.save();
    assert.strictEqual(await fs.readFile(file, 'utf8'), reopened.getText());

    const mutation = vscode.Uri.file(path.join(root, 'vscode-mutation-fixture.txt'));
    const renamed = vscode.Uri.file(path.join(root, 'vscode-mutation-renamed.txt'));
    await vscode.workspace.fs.writeFile(mutation, Buffer.from('vscode mutation fixture\n'));
    await vscode.workspace.fs.rename(mutation, renamed, { overwrite: false });
    assert.strictEqual(Buffer.from(await vscode.workspace.fs.readFile(renamed)).toString(), 'vscode mutation fixture\n');
    await vscode.workspace.fs.delete(renamed);
    await assert.rejects(() => Promise.resolve(vscode.workspace.fs.stat(renamed)));
  });

  test('open, edit, save and reopen a Notebook', async function () {
    const nbRel = process.env.AIDPFS_E2E_NOTEBOOK;
    if (!nbRel || nbRel.includes('/')) {
      this.skip();
      return;
    }
    const nbUri = vscode.Uri.file(path.join(root, nbRel));
    const notebook = await vscode.workspace.openNotebookDocument(nbUri);
    await vscode.window.showNotebookDocument(notebook);
    assert.strictEqual(notebook.notebookType, 'jupyter-notebook');
    assert.strictEqual(notebook.cellCount, 2);
    const cell = notebook.cellAt(1).document;
    assert.ok(cell.getText().includes('aidpfs-vscode-notebook-seed'));

    const edit = new vscode.WorkspaceEdit();
    edit.insert(cell.uri, new vscode.Position(cell.lineCount, 0), '\n# aidpfs-vscode-notebook-saved');
    assert.ok(await vscode.workspace.applyEdit(edit));
    assert.strictEqual(notebook.isDirty, true);
    assert.ok(await notebook.save(), 'Notebook save should succeed on the write mount');
    assert.strictEqual(notebook.isDirty, false);

    const raw = JSON.parse(await fs.readFile(nbUri.fsPath, 'utf8'));
    const source = ([] as string[]).concat(raw.cells[1].source).join('');
    assert.ok(source.includes('aidpfs-vscode-notebook-saved'), 'saved bytes should contain the edit');
    assert.strictEqual(raw.metadata.aidpfs_marker, 'vscode-seed', 'notebook metadata should be preserved');

    await vscode.commands.executeCommand('workbench.action.closeActiveEditor');
    const reopened = await vscode.workspace.openNotebookDocument(nbUri);
    assert.ok(reopened.cellAt(1).document.getText().includes('aidpfs-vscode-notebook-saved'));
    await assert.rejects(() => Promise.resolve(vscode.workspace.fs.delete(nbUri)),
      'Notebook deletion stays unsupported');
  });
});
