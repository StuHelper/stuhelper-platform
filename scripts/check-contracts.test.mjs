import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { contractFiles } from './check-contracts.mjs';

test('contractFiles discovers all OpenAPI definitions including eligibility.openapi.json', () => {
  const files = contractFiles('contracts');
  assert.deepEqual(files, [
    'contracts/admin.openapi.json',
    'contracts/eligibility.openapi.json',
    'contracts/module.openapi.yaml',
    'contracts/platform.openapi.yaml',
  ]);
});

test('contractFiles rejects empty contract directory', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'stuhelper-contracts-empty-'));
  try {
    assert.throws(() => contractFiles(dir), /No OpenAPI contracts/);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
