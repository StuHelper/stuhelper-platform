import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

export function importError(owner, source, target) {
  if (target.startsWith('github.com/StuHelper/stuhelper-') && target !== owner && !target.startsWith(`${owner}/`)) {
    const local = source.slice(owner.length + 1);
    const external = target.split('/').slice(3).join('/');
    const framework = target.startsWith('github.com/StuHelper/stuhelper-platform/') && ['module', 'transport', 'application'].includes(external);
    const contract = /^(api|contracts)(\/|$)/.test(external);
    if (!/^(cmd|examples)\//.test(local) && !framework && !contract) return `Cross-module implementation import: ${source} -> ${target}`;
  }
  const from = source.split('/'); const to = target.split('/');
  if (from.includes('handler') && to.includes('repository')) return `Handler bypasses service: ${source} -> ${target}`;
  if (from.includes('service') && to.includes('handler')) return `Service depends on handler: ${source} -> ${target}`;
  if (from.includes('repository') && to.some(part => ['handler', 'service'].includes(part))) return `Repository depends on an upper layer: ${source} -> ${target}`;
}

export function check() {
  const env = { ...process.env, GOWORK: 'off' };
  const owner = execFileSync('go', ['list', '-m'], { env, encoding: 'utf8' }).trim();
  const packages = execFileSync('go', ['list', '-f', '{{.ImportPath}} {{join .Imports " "}}', './...'], { env, encoding: 'utf8' });
  const errors = packages.trim().split('\n').flatMap(line => {
    const [source, ...imports] = line.trim().split(' ');
    return imports.map(target => importError(owner, source, target)).filter(Boolean);
  });
  if (errors.length) throw new Error(errors.join('\n'));
  console.log('PASS: module and layer import boundaries');
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { check(); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
