import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { parse, stringify } from 'yaml';

export const codegenVersion = 'github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0';

export const contractTargets = [
  {
    name: 'admin',
    spec: 'contracts/admin.openapi.json',
    goOutput: 'sdk/admin/admin.gen.go',
    tsOutput: 'sdk/admin.gen.ts',
    packageName: 'adminsdk',
    generate: 'types,client',
  },
  {
    name: 'eligibility',
    spec: 'contracts/eligibility.openapi.json',
    goOutput: 'sdk/eligibility.gen.go',
    tsOutput: 'sdk/eligibility.gen.ts',
    packageName: 'sdk',
    generate: 'types,client',
  },
  {
    name: 'platform',
    spec: 'contracts/platform.openapi.yaml',
    goOutput: 'internal/api/gen/platform.gen.go',
    tsOutput: 'sdk/platform.gen.ts',
    codegenConfig: 'contracts/oapi-codegen.yaml',
  },
];

export function renderModuleMetadata(source) {
  const { critical, ...descriptor } = parse(source);
  return JSON.stringify({ critical, descriptor }, null, 2) + '\n';
}

export function runGeneration({ check = false, root = process.cwd() } = {}) {
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'stuhelper-generate-'));
  try {
    const moduleJSON = renderModuleMetadata(fs.readFileSync(path.join(root, 'module.yaml'), 'utf8'));
    const modulePath = path.join(root, 'internal/composition/platform.json');
    if (check) {
      assert.equal(fs.readFileSync(modulePath, 'utf8'), moduleJSON, 'Generated module metadata differs');
    } else {
      fs.mkdirSync(path.dirname(modulePath), { recursive: true });
      fs.writeFileSync(modulePath, moduleJSON);
    }

    for (const target of contractTargets) {
      const specPath = path.join(root, target.spec);
      const expectedGo = path.join(root, target.goOutput);
      const expectedTS = path.join(root, target.tsOutput);
      const goOut = check ? path.join(temporary, `${target.name}.gen.go`) : expectedGo;
      const tsOut = check ? path.join(temporary, `${target.name}.gen.ts`) : expectedTS;

      if (!check) {
        fs.mkdirSync(path.dirname(expectedGo), { recursive: true });
        fs.mkdirSync(path.dirname(expectedTS), { recursive: true });
      }

      const goEnv = { ...process.env, GOWORK: 'off' };
      if (target.codegenConfig) {
        const cfg = parse(fs.readFileSync(path.join(root, target.codegenConfig), 'utf8'));
        cfg.output = goOut;
        const tempCfg = path.join(temporary, `${target.name}.oapi-codegen.yaml`);
        fs.writeFileSync(tempCfg, stringify(cfg));
        execFileSync('go', ['run', codegenVersion, '--config', tempCfg, specPath], { cwd: root, env: goEnv, stdio: 'inherit' });
      } else {
        execFileSync('go', ['run', codegenVersion, '-generate', target.generate, '-package', target.packageName, '-o', goOut, specPath], { cwd: root, env: goEnv, stdio: 'inherit' });
      }
      execFileSync('pnpm', ['exec', 'openapi-typescript', specPath, '-o', tsOut], { cwd: root, stdio: 'inherit' });

      if (check) {
        assert.equal(fs.readFileSync(expectedGo, 'utf8'), fs.readFileSync(goOut, 'utf8'), `Generated Go contract differs: ${target.goOutput}`);
        assert.equal(fs.readFileSync(expectedTS, 'utf8'), fs.readFileSync(tsOut, 'utf8'), `Generated TypeScript contract differs: ${target.tsOutput}`);
      }
    }
    console.log(`PASS: ${check ? 'checked' : 'generated'} module metadata and ${contractTargets.length} OpenAPI SDKs`);
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true });
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    runGeneration({ check: process.argv.includes('--check') });
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
