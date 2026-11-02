import * as path from 'path';
import { downloadAndUnzipVSCode, runTests } from '@vscode/test-electron';

async function main(): Promise<void> {
  const workspace = process.env.AIDPFS_E2E_WORKSPACE;
  if (!workspace) throw new Error('AIDPFS_E2E_WORKSPACE is required');
  const executable = await downloadAndUnzipVSCode('stable');
  await runTests({
    vscodeExecutablePath: executable,
    extensionDevelopmentPath: path.resolve(__dirname, '..', '..', 'extension'),
    extensionTestsPath: path.resolve(__dirname, '..', 'test', 'suite', 'index'),
    launchArgs: [workspace, '--disable-gpu', '--no-sandbox', '--disable-workspace-trust',
      '--user-data-dir', '/tmp/aidpfs-vscode/user-data',
      '--extensions-dir', '/tmp/aidpfs-vscode/extensions'],
    extensionTestsEnv: process.env
  });
}

main().catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : 'VS Code test runner failed');
  process.exit(1);
});
