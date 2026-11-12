"""Mechanically bind finite-model applicability to exact changed Git blobs.

This checks declared scope and evidence, not semantic coverage or human review.
Unknown implementation paths fail; a prose reason never exempts runtime code.
"""
import argparse
import fnmatch
import hashlib
import json
import re
import subprocess
from pathlib import Path

from formal_model_gate import GateError, load_json, load_registry, exact_head_inputs, require
from formal_module_metadata import require_metadata_only

RECORD = 'formal/applicability.json'
FORMAL_INFRA = {'formal/models.json', 'formal/tla2tools.sha256',
                'scripts/check-formal.py', 'scripts/formal_model_gate.py',
                'scripts/check-formal-applicability.py', 'scripts/formal_applicability.py',
                'scripts/test-formal-gate.py', 'scripts/test-formal-applicability.py',
                'scripts/formal_module_metadata.py'}
KNOWN_VALIDATION = {
    'scripts/smithy-contracts.py',
    'scripts/software_factory.py',  # Local development evidence/claim gate, never simulator runtime.
    'scripts/check-sts-formal-candidate.py',
    'scripts/build-release.py', 'scripts/verify-release.py', 'scripts/verify-release-tag.py',
    'scripts/build-vulndb-snapshot.py', 'scripts/catalog-matrix.py',
    'scripts/check-assets.py', 'scripts/check-coverage.py', 'scripts/check-cross-build.py',
    'scripts/check-native-provenance.py',
    'scripts/get-audit-tools.py', 'scripts/test-coverage-gate.py', 'scripts/update-go-notices.py',
    'scripts/requirements-test.txt',
}
NONFORMAL = {
    'documentation': ['internal/aws/testdata/sts-original/README.md',
                      'internal/aws/testdata/sts-original/LICENSE', 'README.md', 'README.ja.md', 'SECURITY.md', 'AGENTS.md', 'docs/**', 'formal/README.md',
                      'LICENSE', 'NOTICE', 'third_party/**', 'internal/legal/assets/**'],
    'presentation': ['tools/console/**', 'tools/scalar/**', 'internal/server/web/**',
                     'internal/server/apidocs/**',
                     'internal/server/landing/index.html', 'internal/server/landing/landing.css'],
    'test-support': ['internal/aws/testdata/smithy/LICENSE.txt',
                     'internal/aws/testdata/smithy/NOTICE.txt',
                     'internal/aws/testdata/smithy/source.json',
                     'internal/aws/testdata/smithy/sqs.json',
                     'internal/aws/testdata/smithy/sqs-contracts.json', 'internal/aws/testdata/hello-world-battle-iam-fragment.yaml', 'internal/aws/testdata/sts-original/hello-world.yaml', 'test/**', '*_test.go', 'scripts/test-*.py',
                     'internal/aws/testdata/dynamodb_size_sdk.py'],
    'build-metadata': ['Makefile', '.gitignore', '.gitattributes', '.github/CODEOWNERS',
                       '.github/PULL_REQUEST_TEMPLATE.md'],
}


def git(root, *args, optional=False):
    result = subprocess.run(['git', '-C', str(root), *args], capture_output=True, check=False)
    if result.returncode and optional:
        return None
    require(result.returncode == 0, 'Git could not resolve the requested immutable tree')
    return result.stdout


def commit(root, value):
    require(isinstance(value, str) and re.fullmatch(r'[0-9a-f]{40}', value), 'base must be a full immutable commit SHA')
    actual = git(root, 'rev-parse', '--verify', value + '^{commit}').decode().strip()
    require(actual == value, 'base is not an immutable commit')
    return actual


def blob(root, revision, path):
    require(path and not Path(path).is_absolute() and '..' not in Path(path).parts and not any(c in path for c in '\r\n\t\x00'), 'invalid changed path')
    if revision == ':':
        mode = git(root, 'ls-files', '--stage', '--', path).decode().split()[0]
        data = git(root, 'show', ':' + path)
    else:
        mode = git(root, 'ls-tree', revision, '--', path).decode().split()[0]
        data = git(root, 'show', revision + ':' + path)
    require(mode in ('100644', '100755'), 'symlink/submodule changes require explicit policy work')
    return hashlib.sha256(data).hexdigest()


def changes(root, base, revision='HEAD'):
    command = ['diff', '--name-status', '-z', '--no-renames', base]
    command += ['--cached'] if revision == ':' else [revision]
    fields = git(root, *command).split(b'\0')
    require(fields[-1] == b'', 'malformed Git change list')
    fields.pop()
    require(len(fields) % 2 == 0, 'malformed Git change pairs')
    result = {}
    for status, raw in zip(fields[::2], fields[1::2]):
        path = raw.decode('utf-8')
        if path == RECORD:
            continue  # The record cannot hash itself; all other blobs are bound.
        require(status in (b'A', b'M', b'D'), 'unsupported Git change kind')
        result[path] = None if status == b'D' else blob(root, revision, path)
    return result


def prompt_documentation(path):
    # fnmatch '*' crosses '/', so keep these prompt-only categories one level.
    return path == 'CLAUDE.md' or re.fullmatch(
        r'\.claude/(?:agents/[^/]+\.md|rules/[^/]+\.md|skills/[^/]+/SKILL\.md)', path) is not None


def disposition(path):
    if prompt_documentation(path):
        return 'documentation'
    if path in FORMAL_INFRA or path.startswith('.github/workflows/'):
        return 'formal-infrastructure'
    if path in KNOWN_VALIDATION:
        return 'validation-tooling'
    for kind, patterns in NONFORMAL.items():
        if any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns):
            return kind
    return None


def module_metadata_change(root, base):
    # Never classify from the index/worktree or trust only a prose claim. Both
    # files must already exist as ordinary blobs on both sides of this range.
    inputs = []
    for revision in (base, 'HEAD'):
        pair = []
        for path in ('go.mod', 'go.sum'):
            entry = git(root, 'ls-tree', revision, '--', path).decode().split()
            if not entry or entry[0] != '100644':
                return False
            pair.append(git(root, 'show', revision + ':' + path))
        inputs.append(pair)
    return require_metadata_only(inputs[0][0], inputs[1][0], inputs[0][1], inputs[1][1])


def base_models(root, base):
    data = git(root, 'show', base + ':formal/models.json', optional=True)
    if data is None:
        return {}  # One-time bootstrap before the registry existed.
    try:
        registry = load_json(data)
        return {m['id']: m for m in registry['models']}
    except (ValueError, KeyError, TypeError) as error:
        raise GateError('base registry is malformed') from error


def validate_record(root, base, registry=None):
    root = Path(root)
    snapshot = exact_head_inputs(root)
    base = commit(root, base)
    registry = registry or load_registry(root)
    data = git(root, 'show', 'HEAD:' + RECORD)
    try:
        record = load_json(data)
    except ValueError as error:
        raise GateError('malformed applicability record') from error
    require(set(record) == {'version', 'base_sha', 'changes'} and type(record['version']) is int and record['version'] == 1, 'invalid applicability record schema (no claimed reviewer identities)')
    require(record['base_sha'] == base, 'stale applicability base; regenerate after rebase')
    require(isinstance(record['changes'], list), 'applicability changes must be a list')
    actual = changes(root, base)
    prior, models = base_models(root, base), {m['id']: m for m in registry['models']}
    require(set(prior) <= set(models), 'registered models cannot silently disappear')
    for mid in prior:
        require(set(prior[mid]['properties']) <= set(models[mid]['properties']), 'registered invariants cannot silently disappear')
    seen = set()
    evidence = []
    for item in record['changes']:
        require(isinstance(item, dict) and set(item) == {'path', 'sha256', 'disposition', 'reason', 'models'}, 'invalid applicability entry')
        path = item['path']
        require(isinstance(path, str) and path not in seen and path in actual, 'extra or duplicate applicability path')
        seen.add(path)
        require(item['sha256'] == actual[path], 'stale applicability blob: ' + path)
        if prompt_documentation(path):
            for revision in (base, 'HEAD'):
                entry = git(root, 'ls-tree', revision, '--', path).decode().split()
                require(not entry or entry[0] == '100644',
                        'prompt documentation must be regular nonexecutable Markdown: ' + path)
        require(isinstance(item['reason'], str) and item['reason'].strip(), 'an explicit applicability argument is required: ' + path)
        require(isinstance(item['models'], list), 'model bindings must be a list')
        if item['disposition'] == 'modeled':
            require(item['models'], 'runtime/state changes need model bindings: ' + path)
            bound = set()
            for binding in item['models']:
                require(isinstance(binding, dict) and set(binding) == {'id', 'properties', 'limits'}, 'invalid model binding')
                mid = binding['id']
                require(mid in models and mid not in bound, 'missing or duplicate bound model')
                bound.add(mid)
                model = models[mid]
                scope = set(model['source_scope']) | {model['model'], model['config']}
                if actual[path] is None and mid in prior:
                    scope |= set(prior[mid]['source_scope'])
                require(path in scope, 'model does not declare this implementation/artifact scope: ' + path)
                require(isinstance(binding['properties'], list) and set(binding['properties']) - {'TypeOK'} and set(binding['properties']) <= set(model['properties']), 'binding names an unchecked property')
                require(isinstance(binding['limits'], str) and binding['limits'].strip(), 'binding must acknowledge what is not modeled')
        else:
            kind = disposition(path)
            module_metadata_only = False
            if path == 'go.mod' and item['disposition'] == 'build-metadata':
                # A dependency can be model-scoped while a later exact
                # require regrouping/indirect annotation changes no dependency.
                # Keep the immutable parser and unchanged-go.sum check; versions,
                # additions, directives, mode changes and other paths still fail.
                module_metadata_only = module_metadata_change(root, base)
                require(module_metadata_only, 'go.mod is not a recognized require-annotation/grouping-only change with unchanged go.sum')
                kind = 'build-metadata'
            require(module_metadata_only or not any(path in m['source_scope'] or path in (m['model'], m['config']) for m in [*models.values(), *prior.values()]), 'declared state/model scope requires a modeled disposition: ' + path)
            require(kind is not None, 'unclassified implementation/state path requires a model: ' + path)
            require(item['disposition'] == kind and not item['models'], 'nonformal disposition mismatch: ' + path)
        evidence.append(item)
    require(seen == set(actual), 'missing applicability entries: ' + ', '.join(sorted(set(actual) - seen)))
    after = exact_head_inputs(root)
    require(after['head'] == snapshot['head'] and after['hashes'] == snapshot['hashes'], 'applicability inputs changed during validation')
    return {'input_mode': 'immutable-head', 'input_sha256': snapshot['hashes'], 'kind': 'declared-applicability-not-semantic-proof', 'base_sha': base,
            'head_sha': git(root, 'rev-parse', 'HEAD').decode().strip(), 'changes': evidence,
            'review': 'Human acceptance is external; this check does not authenticate a reviewer or prove refinement.'}


def initialize(root, base):
    root = Path(root)
    base = commit(root, base)
    staged = changes(root, base, ':')
    entries = [{'path': path, 'sha256': sha, 'disposition': 'needs-review', 'reason': '', 'models': []}
               for path, sha in sorted(staged.items())]
    (root / RECORD).write_text(json.dumps({'version': 1, 'base_sha': base, 'changes': entries}, indent=2) + '\n')
    return len(entries)


def event_base(root, event):
    if 'pull_request' in event:
        return commit(root, event['pull_request']['base']['sha'])
    if 'merge_group' in event:
        return commit(root, event['merge_group']['base_sha'])
    if event.get('ref') == 'refs/heads/main' and event.get('before'):
        return commit(root, event['before'])
    common = git(root, 'merge-base', 'refs/remotes/origin/main', 'HEAD').decode().strip()
    head = git(root, 'rev-parse', 'HEAD').decode().strip()
    # A manual replay of current main checks its actual preceding main tree.
    if common == head:
        common = git(root, 'rev-parse', 'HEAD^').decode().strip()
    return commit(root, common)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    for name in ('init', 'check'):
        command = sub.add_parser(name)
        command.add_argument('--base', required=True)
    sub.add_parser('event-base').add_argument('--event', required=True)
    options = parser.parse_args()
    try:
        root = Path.cwd()
        if options.command == 'init':
            print(f'Wrote {initialize(root, options.base)} unresolved entries; author must explain each disposition before committing.')
        elif options.command == 'event-base':
            print(event_base(root, json.loads(Path(options.event).read_text())))
        else:
            evidence = validate_record(root, options.base)
            directory = root / 'reports/formal'
            directory.mkdir(parents=True, exist_ok=True)
            (directory / 'applicability.json').write_text(json.dumps(evidence, indent=2) + '\n')
            print(f'Exact blob applicability validated for {len(evidence["changes"])} changed files; semantic/human review remains separate.')
    except (GateError, OSError, ValueError, KeyError, TypeError, IndexError) as error:
        parser.exit(1, 'Formal applicability failed: ' + str(error) + '\n')
