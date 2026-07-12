type Severity = 'error' | 'warning';
type RuleGroup = 'pre-release';

const APPLICATION_SOURCE_ROOTS = [
  'contracts',
  'core',
  'providers',
  'apps',
  'tools',
  'conformance',
  'scripts',
] as const;

const DEFAULT_SOURCE_EXTENSION = /\.(ts|tsx|js|jsx)$/;
const TEST_ARTIFACT =
  /\.(test|spec)\.[^/]+$|(^|\/)(__fixtures__|fixtures?|__mocks__|mocks?|__tests__|tests?)(\/|$)/;
const GENERATED_ARTIFACT =
  /(^|\/)(__generated__|generated)(\/|$)|\.generated\.[^/]+$/;

function isApplicationSource(
  filePath: string,
  extension: RegExp = DEFAULT_SOURCE_EXTENSION
): boolean {
  return (
    APPLICATION_SOURCE_ROOTS.some((root) => filePath.startsWith(`${root}/`)) &&
    extension.test(filePath) &&
    !TEST_ARTIFACT.test(filePath) &&
    !GENERATED_ARTIFACT.test(filePath)
  );
}

interface Finding {
  rule: string;
  severity: Severity;
  file: string;
  line?: number;
  message: string;
}

interface Rule {
  id: string;
  description: string;
  groups?: readonly RuleGroup[];
  standalone?: boolean;
  scope: (filePath: string) => boolean;
  check: (file: { path: string; content: string }) => Finding[];
}

interface RepoCheck {
  id: string;
  description: string;
  check: (root: string) => Promise<Finding[]>;
}

export type { Finding, RepoCheck, Rule, RuleGroup, Severity };
export { APPLICATION_SOURCE_ROOTS, isApplicationSource };
