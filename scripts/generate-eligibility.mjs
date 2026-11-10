import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';

const check = process.argv.includes('--check');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'stuhelper-eligibility-generate-'));
try {
  fs.mkdirSync('sdk', { recursive: true });
  const goOutput = check ? path.join(temporary, 'eligibility.gen.go') : 'sdk/eligibility.gen.go';
  const tsOutput = check ? path.join(temporary, 'eligibility.gen.ts') : 'sdk/eligibility.gen.ts';
  execFileSync('go', ['run', 'github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0', '-generate', 'types,client', '-package', 'sdk', '-o', goOutput, 'contracts/eligibility.openapi.json'], { stdio: 'inherit' });
  execFileSync('pnpm', ['exec', 'openapi-typescript', 'contracts/eligibility.openapi.json', '-o', tsOutput], { stdio: 'inherit' });
  if (check) {
    for (const [expected, actual] of [['sdk/eligibility.gen.go', goOutput], ['sdk/eligibility.gen.ts', tsOutput]]) {
      assert.equal(fs.readFileSync(expected, 'utf8'), fs.readFileSync(actual, 'utf8'), `Generated contract differs: ${expected}`);
    }
  }
} finally {
  fs.rmSync(temporary, { recursive: true, force: true });
}
