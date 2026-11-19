import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const publicPlatformPackages = new Set([
  'account',
  'admin',
  'admin/httpapi',
  'application',
  'eligibility',
  'eligibility/httpapi',
  'module',
  'modulehistory',
  'sdk',
  'sdk/admin',
  'transport',
]);

const domainPackages = new Set(['account', 'admin', 'eligibility', 'module', 'modulehistory']);

function isHandler(parts) {
  return parts.includes('handler') || parts.includes('httpapi') || parts[0] === 'transport';
}

function isRepository(parts) {
  return parts.includes('repository') || parts.includes('postgres');
}

function isUpperRuntime(parts) {
  return parts[0] === 'application' || parts[0] === 'sdk';
}

export function importError(owner, source, target) {
  if (target.startsWith('github.com/StuHelper/stuhelper-') && target !== owner && !target.startsWith(`${owner}/`)) {
    const local = source === owner ? '' : source.slice(owner.length + 1);
    const external = target.split('/').slice(3).join('/');
    const framework = target.startsWith('github.com/StuHelper/stuhelper-platform/') && publicPlatformPackages.has(external);
    const contract = /^(api|contracts)(\/|$)/.test(external);
    if (!/^(cmd|examples)\//.test(local) && !framework && !contract) return `Cross-module implementation import: ${source} -> ${target}`;
    return undefined;
  }
  if (target === owner || target.startsWith(`${owner}/`)) {
    const fromParts = (source === owner ? '' : source.slice(owner.length + 1)).split('/').filter(Boolean);
    const toParts = (target === owner ? '' : target.slice(owner.length + 1)).split('/').filter(Boolean);
    if (fromParts[0] === 'cmd' || fromParts[0] === 'examples') return undefined;
    const fromDomainOrService = fromParts.includes('service') || (domainPackages.has(fromParts[0]) && !isHandler(fromParts) && !isRepository(fromParts));
    if (isHandler(fromParts) && isRepository(toParts)) return `Handler bypasses service: ${source} -> ${target}`;
    if (fromDomainOrService && (isHandler(toParts) || isUpperRuntime(toParts))) return `Service depends on handler: ${source} -> ${target}`;
    if (fromDomainOrService && toParts.includes('postgres')) return `Service depends on concrete postgres adapter: ${source} -> ${target}`;
    if (isRepository(fromParts) && (isHandler(toParts) || isUpperRuntime(toParts) || toParts.includes('service'))) return `Repository depends on an upper layer: ${source} -> ${target}`;
  }
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
