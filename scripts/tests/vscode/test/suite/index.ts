import * as path from 'path';
import Mocha = require('mocha');

export function run(): Promise<void> {
  const mocha = new Mocha({ ui: 'tdd', color: true, timeout: 120_000 });
  mocha.addFile(path.resolve(__dirname, 'workspace.test.js'));
  return new Promise((resolve, reject) => mocha.run((failures: number) =>
    failures ? reject(new Error(`${failures} VS Code E2E test(s) failed`)) : resolve()));
}
