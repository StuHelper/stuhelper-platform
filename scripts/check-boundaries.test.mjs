import test from 'node:test';
import assert from 'node:assert/strict';
import { importError } from './check-boundaries.mjs';
const owner = 'github.com/StuHelper/stuhelper-reviews';
const catalog = 'github.com/StuHelper/stuhelper-catalog';
test('business modules use contracts and public framework APIs, never another implementation', () => {
  assert.equal(importError(owner, `${owner}/service`, `${catalog}/api`), undefined);
  assert.match(importError(owner, `${owner}/service`, `${catalog}/repository`), /Cross-module/);
  assert.equal(importError(owner, `${owner}/module`, 'github.com/StuHelper/stuhelper-platform/module'), undefined);
  assert.equal(importError(owner, `${owner}/handler`, 'github.com/StuHelper/stuhelper-platform/transport'), undefined);
  assert.match(importError(owner, `${owner}/service`, 'github.com/StuHelper/stuhelper-platform/internal/composition'), /Cross-module/);
  assert.equal(importError(owner, `${owner}/cmd/server`, `${catalog}/module`), undefined);
});
test('HTTP adapters cannot skip services and repositories cannot depend on upper layers', () => {
  assert(importError(owner, `${owner}/handler`, `${owner}/repository`));
  assert(importError(owner, `${owner}/repository`, `${owner}/service`));
  assert(importError(owner, `${owner}/service`, `${owner}/handler`));
  assert.equal(importError(owner, `${owner}/service`, `${owner}/repository`), undefined);
});
