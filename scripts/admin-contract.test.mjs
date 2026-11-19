import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

test('role capability schema requires a namespace and nonempty segments', () => {
  const document = JSON.parse(readFileSync(new URL('../contracts/admin.openapi.json', import.meta.url), 'utf8'));
  const schema = document.components.schemas.AdminRole.properties.capabilities.items;
  const pattern = new RegExp(schema.pattern);
  for (const value of ['platform.audit.read', 'information-query.group_read']) {
    assert.ok(pattern.test(value), value);
  }
  for (const value of ['platform', 'platform.', '.read', 'a..b', 'a.-b']) {
    assert.equal(pattern.test(value), false, value);
  }
  assert.equal(schema.maxLength, 128);
});
