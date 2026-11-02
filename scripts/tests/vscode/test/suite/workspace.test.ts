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

  test('open, search, edit, save, reopen, preserve dirty buffer, and reject rename/delete', async () => {
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

    await assert.rejects(() => Promise.resolve(vscode.workspace.fs.rename(uri, vscode.Uri.file(`${file}.renamed`))));
    await assert.rejects(() => Promise.resolve(vscode.workspace.fs.delete(uri)));
    assert.ok((await fs.readFile(file, 'utf8')).includes('aidpfs-vscode-saved'));
    assert.strictEqual(reopened.isDirty, true);
    reopened.isDirty && await vscode.workspace.fs.writeFile(uri, Buffer.from(reopened.getText()));
    await reopened.save();
    assert.strictEqual(await fs.readFile(file, 'utf8'), reopened.getText());
  });
});
