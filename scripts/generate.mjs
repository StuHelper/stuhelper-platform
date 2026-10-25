import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { parse, stringify } from 'yaml';

const check = process.argv.includes('--check');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'stuhelper-generate-'));
try {
  const { critical, ...descriptor } = parse(fs.readFileSync('module.yaml', 'utf8'));
  const moduleJSON = JSON.stringify({ critical, descriptor }, null, 2) + '\n';
  const config = parse(fs.readFileSync('contracts/oapi-codegen.yaml', 'utf8'));
  const goOutput = check ? path.join(temporary, 'platform.gen.go') : path.resolve(config.output);
  const tsOutput = check ? path.join(temporary, 'platform.gen.ts') : path.resolve('sdk/platform.gen.ts');
  config.output = goOutput;
  const configuration = path.join(temporary, 'oapi-codegen.yaml'); fs.writeFileSync(configuration, stringify(config));
  execFileSync('go', ['run', 'github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0', '--config', configuration, 'contracts/platform.openapi.yaml'], { stdio: 'inherit' });
  execFileSync('pnpm', ['exec', 'openapi-typescript', 'contracts/platform.openapi.yaml', '-o', tsOutput], { stdio: 'inherit' });
  if (check) {
    assert.equal(fs.readFileSync('internal/composition/platform.json', 'utf8'), moduleJSON, 'Generated module metadata differs');
    assert.equal(fs.readFileSync('internal/api/gen/platform.gen.go', 'utf8'), fs.readFileSync(goOutput, 'utf8'), 'Generated Go contract differs');
    assert.equal(fs.readFileSync('sdk/platform.gen.ts', 'utf8'), fs.readFileSync(tsOutput, 'utf8'), 'Generated TypeScript contract differs');
  } else fs.writeFileSync('internal/composition/platform.json', moduleJSON);
  console.log(`PASS: ${check ? 'checked' : 'generated'} module metadata and API types`);
} catch (error) { console.error(error.message); process.exitCode = 1; }
finally { fs.rmSync(temporary, { recursive: true, force: true }); }
