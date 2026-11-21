// Human-readable names for catalog resource kinds and official members,
// derived deterministically from the official names: words are split at
// case changes, sentence-cased, and known acronyms keep their spelling.
// There are no per-service label tables.

const ACRONYMS = new Map(['acl', 'acm', 'ai', 'ami', 'api', 'apis', 'arn', 'arns', 'asl', 'aws', 'az', 'azs', 'ca', 'cidr', 'cpu', 'cpus', 'csv', 'db', 'dns', 'dsql',
  'ebs', 'ec2', 'ecr', 'ecs', 'efs', 'eks', 'emr', 'etl', 'fsx', 'gpu', 'gpus', 'hsm', 'http', 'https', 'iam', 'id', 'ids', 'iops', 'iot', 'ip', 'ips', 'json', 'jwt',
  'kms', 'mfa', 'ml', 'mq', 'nat', 'nfs', 'oidc', 'os', 'qos', 'rds', 'saml', 'sdk', 'sms', 'sns', 'sql', 'sqs', 'ssh', 'ssl', 'sso', 'tls', 'ttl', 'ui', 'uri', 'url',
  'urls', 'utc', 'uuid', 'vpc', 'vpcs', 'vpn', 'waf', 'xml', 'yaml'].map(word => [word, word.toUpperCase()]));
// Mixed-case spellings that a plain upper-casing would break.
for (const [word, spelling] of [['ids', 'IDs'], ['arns', 'ARNs'], ['apis', 'APIs'], ['azs', 'AZs'], ['cpus', 'CPUs'], ['gpus', 'GPUs'], ['ips', 'IPs'], ['urls', 'URLs'],
  ['vpcs', 'VPCs'], ['ipv4', 'IPv4'], ['ipv6', 'IPv6'], ['oauth', 'OAuth'], ['mysql', 'MySQL'], ['postgresql', 'PostgreSQL'], ['graphql', 'GraphQL'], ['iops', 'IOPS']]) {
  ACRONYMS.set(word, spelling);
}

// words splits an official name into its lower-case words. A run of
// capitals followed by a capitalized word ends one letter early
// (DBSubnet -> DB, Subnet), and IPv4/IPv6 and OAuth stay whole.
export function words(name) {
  const text = String(name ?? '').replace(/IPv([46])/g, ' ipv$1 ').replace(/OAuth/g, ' oauth ').replace(/[_\-.:/]+/g, ' ');
  const parts = text.match(/[A-Z]+(?=[A-Z][a-z])|[A-Z]?[a-z]+[0-9]*|[A-Z]+[0-9]*|[0-9]+[a-z]*/g) || [];
  return parts.map(part => part.toLowerCase());
}

// humanize turns an official name into a sentence-case label:
// DBSubnetGroupName -> "DB subnet group name", VpcId -> "VPC ID".
export function humanize(name) {
  const list = words(name);
  if (!list.length) return String(name ?? '');
  return list.map((word, i) => ACRONYMS.get(word) ?? (i === 0 ? word[0].toUpperCase() + word.slice(1) : word)).join(' ');
}

// plural makes the last word plural: group -> groups, policy -> policies,
// alias -> aliases. Acronyms take a lower-case s (ACLs).
export function plural(label) {
  const match = /^(.*?)(\S+)$/.exec(label);
  if (!match) return label;
  const [, head, last] = match;
  if (/^[A-Z0-9]+s$/.test(last)) return label;
  if (last === last.toUpperCase() && /[A-Z]/.test(last)) return head + (last.endsWith('S') ? last : last + 's');
  if (/(?:s|x|z|ch|sh)$/.test(last)) return head + last + 'es';
  if (/[^aeiou]y$/.test(last)) return head + last.slice(0, -1) + 'ies';
  return head + last + 's';
}

// Columns rank the most informative scalar members of listed rows: state,
// then kind and version, then description and times, then the rest.
const RANK = [/status|state$/i, /type$|family$|engine|version|class$|mode$/i, /description/i, /time$|date$|created|modified|updated/i];

// pickColumns chooses up to `limit` scalar keys present in the rows, other
// than the identity keys the table already shows.
export function pickColumns(rows, exclude, limit = 3) {
  const seen = new Map();
  for (const row of rows.slice(0, 50)) {
    for (const [key, value] of Object.entries(row || {})) {
      if (exclude.includes(key) || value === null || value === undefined || value === '' || typeof value === 'object') continue;
      seen.set(key, (seen.get(key) || 0) + 1);
    }
  }
  const rank = key => { const i = RANK.findIndex(re => re.test(key)); return i < 0 ? RANK.length : i; };
  return [...seen.keys()].sort((a, b) => rank(a) - rank(b) || seen.get(b) - seen.get(a) || (a < b ? -1 : a > b ? 1 : 0)).slice(0, limit);
}

// tableShape reports whether a value is a list of structures whose members
// are scalars (or short scalar lists), so it reads best as a table; it
// returns the column keys in first-seen order, or null.
export function tableShape(value) {
  if (!Array.isArray(value) || !value.length || !value.every(item => item && typeof item === 'object' && !Array.isArray(item))) return null;
  const keys = [];
  for (const item of value) {
    for (const [key, cell] of Object.entries(item)) {
      if (cell !== null && typeof cell === 'object' && !(Array.isArray(cell) && cell.every(v => v === null || typeof v !== 'object'))) return null;
      if (!keys.includes(key)) keys.push(key);
    }
  }
  return keys;
}

// matches reports whether a row's scalar values contain the filter text.
export function matches(row, text) {
  const needle = String(text || '').trim().toLowerCase();
  if (!needle) return true;
  return Object.values(row || {}).some(value => value !== null && typeof value !== 'object' && String(value).toLowerCase().includes(needle));
}

// phrase lower-cases a label's first word for use inside a sentence
// ("Create user", "No DB subnet groups") unless it is an acronym.
export function phrase(label) {
  const match = /^(\S+)(.*)$/.exec(String(label ?? ''));
  if (!match) return String(label ?? '');
  const [, first, rest] = match;
  return (first.length > 1 && first.slice(1) !== first.slice(1).toLowerCase() ? first : first.toLowerCase()) + rest;
}
