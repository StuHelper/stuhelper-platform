import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { contractTargets, renderModuleMetadata } from './generate.mjs';

test('declarative generation config covers admin, eligibility and platform contracts without duplicate outputs', () => {
  assert.deepEqual(contractTargets.map(t => t.name), ['admin', 'eligibility', 'platform']);
  const outputs = contractTargets.flatMap(t => [t.goOutput, t.tsOutput]);
  assert.equal(new Set(outputs).size, outputs.length);
  for (const target of contractTargets) {
    assert(fs.existsSync(target.spec), `missing contract: ${target.spec}`);
  }
});

test('renderModuleMetadata separates critical flag from module descriptor', () => {
  const rendered = JSON.parse(renderModuleMetadata('id: platform\napi: 1\ncritical: true\ncapabilities:\n  - platform.status\n'));
  assert.equal(rendered.critical, true);
  assert.equal(rendered.descriptor.id, 'platform');
  assert.equal('critical' in rendered.descriptor, false);
});
