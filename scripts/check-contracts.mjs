import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const tool = ['run', 'github.com/oasdiff/oasdiff@v1.34.1'];

export function contractFiles(dir = 'contracts') {
  const files = fs.readdirSync(dir)
    .filter(name => /\.openapi\.(yaml|yml|json)$/.test(name))
    .sort()
    .map(name => path.posix.join(dir, name));
  if (files.length === 0) throw new Error(`No OpenAPI contracts found in ${dir}`);
  return files;
}

export function checkContracts(base = process.env.CONTRACT_BASE_REF || 'HEAD') {
  const initial = /^0+$/.test(base);
  if (!initial) execFileSync('git', ['rev-parse', '--verify', `${base}^{commit}`], { stdio: 'ignore' });
  for (const file of contractFiles()) {
    execFileSync('go', [...tool, 'validate', file, '--allow-external-refs=false', '--fail-on', 'WARN'], { stdio: 'inherit' });
    if (initial) continue;
    const previous = spawnSync('git', ['show', `${base}:${file}`], { encoding: 'utf8' });
    if (previous.status !== 0) {
      if (execFileSync('git', ['ls-tree', '--name-only', base, '--', file], { encoding: 'utf8' }).trim()) throw new Error(`Cannot read prior contract: ${file}`);
      continue;
    }
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'stuhelper-contract-'));
    try {
      const prior = path.join(directory, `base${path.extname(file)}`);
      fs.writeFileSync(prior, previous.stdout);
      // The owner approved only the capability pattern changes listed for the v6 migration.
      const exceptions = file === 'contracts/admin.openapi.json' ? ['--warn-ignore', 'contracts/admin-capability-migration.warn-ignore'] : [];
      execFileSync('go', [...tool, 'breaking', prior, file, '--allow-external-refs=false', '--fail-on', 'WARN', ...exceptions], { stdio: 'inherit' });
    } finally {
      fs.rmSync(directory, { recursive: true, force: true });
    }
  }
  console.log('PASS: OpenAPI definitions and base compatibility');
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { checkContracts(); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
