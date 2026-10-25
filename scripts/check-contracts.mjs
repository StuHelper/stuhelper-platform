import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';

const tool = ['run', 'github.com/oasdiff/oasdiff@v1.34.1'];
const base = process.env.CONTRACT_BASE_REF || 'HEAD';
try {
  const initial = /^0+$/.test(base);
  if (!initial) execFileSync('git', ['rev-parse', '--verify', `${base}^{commit}`], { stdio: 'ignore' });
  for (const name of ['platform', 'module']) {
    const file = `contracts/${name}.openapi.yaml`;
    execFileSync('go', [...tool, 'validate', file, '--allow-external-refs=false', '--fail-on', 'WARN'], { stdio: 'inherit' });
    if (initial) continue;
    const previous = spawnSync('git', ['show', `${base}:${file}`], { encoding: 'utf8' });
    if (previous.status !== 0) {
      if (execFileSync('git', ['ls-tree', '--name-only', base, '--', file], { encoding: 'utf8' }).trim()) throw new Error(`Cannot read prior contract: ${file}`);
      continue;
    }
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'stuhelper-contract-'));
    try {
      const prior = path.join(directory, 'base.yaml'); fs.writeFileSync(prior, previous.stdout);
      execFileSync('go', [...tool, 'breaking', prior, file, '--allow-external-refs=false', '--fail-on', 'WARN'], { stdio: 'inherit' });
    } finally { fs.rmSync(directory, { recursive: true, force: true }); }
  }
  console.log('PASS: OpenAPI definitions and base compatibility');
} catch (error) { console.error(error.message); process.exitCode = 1; }
