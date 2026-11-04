"""Fail-closed finite TLC validation; no implementation/refinement proof."""
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import tempfile
from pathlib import Path

BASE_MODELS = {'ObjectStore', 'StackOwnership', 'S3Versions', 'NativeIAMOwnership', 'RESTDeploymentSnapshot', 'SSMParameterState', 'StartupTokenVisibility'}
BASE_INVARIANTS = {
    'ObjectStore': {'TypeOK', 'NoOrphans', 'NoUnauthorizedMutation', 'DenyWins'},
    'StackOwnership': {'TypeOK', 'OwnershipExclusive'},
    'S3Versions': {'TypeOK', 'NoHistoryOrphans', 'CurrentMatchesLatest'},
    'NativeIAMOwnership': {'TypeOK', 'OwnershipExclusive'},
    'RESTDeploymentSnapshot': {'TypeOK', 'SnapshotsFrozen', 'NoStageOrphans'},
    'StartupTokenVisibility': {'TypeOK', 'FileIdentityFrozen', 'CreatedFilePrivate', 'HistoryVisibility', 'InvalidFileNeverEmits'},
    'SSMParameterState': {'TypeOK', 'DeleteEffect', 'OtherKeysUnchanged', 'OverwriteRefusal', 'ReadIsolation', 'DeniedPreservesState', 'PutVersion'},
}
IDENTIFIER = re.compile(r'^[A-Za-z][A-Za-z0-9_]*$')

# TLC1.7.4 ModelConfig.ALL_KEYWORDS. A directive cannot masquerade as a
# user invariant even when placed on the same physical CFG line.
CFG_KEYWORDS = {'CONSTANT', 'CONSTANTS', 'CONSTRAINT', 'CONSTRAINTS',
                'ACTION_CONSTRAINT', 'ACTION_CONSTRAINTS', 'INVARIANT', 'INVARIANTS',
                'INIT', 'NEXT', 'VIEW', 'SYMMETRY', 'SPECIFICATION', 'PROPERTY',
                'PROPERTIES', 'TYPE', 'TYPE_CONSTRAINT', 'CHECK_DEADLOCK'}


class GateError(ValueError):
    pass


def digest(data):
    return hashlib.sha256(data).hexdigest()


def require(condition, message):
    if not condition:
        raise GateError(message)


def load_json(data):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, 'duplicate JSON field')
            result[key] = value
        return result
    def reject_constant(value):
        raise GateError('non-finite JSON constant: ' + value)
    return json.loads(data, object_pairs_hook=unique, parse_constant=reject_constant)


def asset(root, name):
    require(isinstance(name, str) and name and not Path(name).is_absolute(), 'asset path must be relative')
    path = root / name
    require(path.resolve().is_relative_to(root.resolve()) and not path.is_symlink(), 'asset escapes repository')
    require(path.is_file() and path.stat().st_size > 0, 'missing or empty asset: ' + name)
    return path


def invariants(config):
    # Deliberately small CFG grammar: named invariants, init/next/specification,
    # Boolean deadlock setting and finite literal constant assignments only.
    # Views/constraints/substitutions cannot silently become fake invariants or
    # prune the checked behavior. Extend this grammar only with explicit review.
    names, constants = [], False
    literal = r'(?:[A-Za-z][A-Za-z0-9_]*|[0-9]+|"(?:[^"\\]|\\.)*")'
    value = rf'(?:{literal}|\{{(?:\s*{literal}(?:\s*,\s*{literal})*\s*)?\}})'
    assignment = re.compile(rf'^[A-Za-z][A-Za-z0-9_]*\s*=\s*{value}$')
    for raw in config.splitlines():
        line = raw.split('\\*', 1)[0].strip()
        if not line:
            continue
        words = line.split()
        if words[0] in ('INVARIANT', 'INVARIANTS'):
            require(len(words) > 1 and all(IDENTIFIER.fullmatch(n) and n not in CFG_KEYWORDS for n in words[1:]), 'invalid named invariants')
            names.extend(words[1:])
            constants = False
        elif words[0] in ('INIT', 'NEXT', 'SPECIFICATION'):
            require(len(words) == 2 and IDENTIFIER.fullmatch(words[1]), 'invalid model behavior declaration')
            constants = False
        elif words[0] == 'CHECK_DEADLOCK':
            require(len(words) == 2 and words[1] in ('TRUE', 'FALSE'), 'invalid deadlock setting')
            constants = False
        elif words[0] in ('CONSTANT', 'CONSTANTS'):
            constants = True
            remainder = line[len(words[0]):].strip()
            require(not remainder or assignment.fullmatch(remainder), 'unsupported constant declaration')
        else:
            require(constants and assignment.fullmatch(line), 'unsupported CFG directive or continuation: ' + words[0])
    require(names and len(names) == len(set(names)), 'invalid or absent config invariants')
    return names


def load_registry(root, relative='formal/models.json'):
    root = Path(root)
    registry_path = asset(root, relative)
    try:
        registry = load_json(registry_path.read_text())
    except (ValueError, UnicodeError) as error:
        raise GateError('invalid model registry') from error
    require(set(registry) == {'version', 'tool', 'models'} and type(registry['version']) is int and registry['version'] == 1, 'unsupported model registry schema')
    tool = registry['tool']
    require(set(tool) == {'version', 'checksum_file', 'release_url'} and tool['version'] == '1.7.4', 'unsupported TLC tool declaration')
    checksum = asset(root, tool['checksum_file']).read_text().strip().split()
    require(len(checksum) == 2 and re.fullmatch(r'[a-f0-9]{64}', checksum[0]) and checksum[1] == 'tla2tools.jar', 'invalid TLC checksum declaration')
    require(tool['release_url'] == 'https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar', 'unexpected TLC download origin')
    models = registry['models']
    require(isinstance(models, list) and models, 'empty model registry')
    ids, files = set(), set()
    for model in models:
        require(set(model) == {'id', 'model', 'config', 'bounds', 'properties', 'limits', 'source_scope', 'mutants'}, 'invalid model fields')
        name = model['id']
        require(isinstance(name, str) and IDENTIFIER.fullmatch(name) and name not in ids, 'invalid or duplicate model id')
        ids.add(name)
        require(model['model'] == f'formal/{name}.tla' and model['config'] == f'formal/{name}.cfg', 'model/config path mismatch')
        source, config = asset(root, model['model']).read_text(), asset(root, model['config']).read_text()
        files.update((model['model'], model['config']))
        require(len(re.findall(r'\bMODULE\s+' + re.escape(name) + r'\b', source)) == 1, 'model module declaration mismatch')
        require(isinstance(model['bounds'], str) and model['bounds'].strip() and isinstance(model['limits'], str) and model['limits'].strip(), 'model bounds/limits must be explicit')
        require(isinstance(model['properties'], list) and len(model['properties']) == len(set(model['properties'])) and set(model['properties']) == set(invariants(config)), 'registry/config invariants mismatch')
        require(BASE_INVARIANTS.get(name, set()) <= set(model['properties']), 'an existing required invariant was removed')
        require(isinstance(model['source_scope'], list) and all(isinstance(p, str) and p and not Path(p).is_absolute() and '..' not in Path(p).parts and not any(c in p for c in '*?[') for p in model['source_scope']), 'source scope must name exact reviewed paths')
        require(len(set(model['source_scope'])) == len(model['source_scope']), 'duplicate model source scope')
        for path in model['source_scope']:
            asset(root, path)
        require(isinstance(model['mutants'], list) and model['mutants'], 'model needs a sensitivity control')
        mutant_ids = set()
        for mutant in model['mutants']:
            require(set(mutant) == {'id', 'edits', 'expect_invariant'}, 'invalid mutant fields')
            require(isinstance(mutant['id'], str) and IDENTIFIER.fullmatch(mutant['id']) and mutant['id'] not in mutant_ids, 'invalid mutant id')
            mutant_ids.add(mutant['id'])
            require(mutant['expect_invariant'] in model['properties'], 'mutant expects unchecked invariant')
            apply_mutant(source, name, mutant)
    require(BASE_MODELS <= ids, 'a required existing model was removed')
    actual = {p.relative_to(root).as_posix() for suffix in ('*.tla', '*.cfg') for p in (root / 'formal').glob(suffix)}
    require(actual == files, 'unregistered or missing model/config artifact')
    registry['_checksum'] = checksum[0]
    registry['_digest'] = digest(registry_path.read_bytes())
    return registry


def apply_mutant(source, name, mutant):
    require(isinstance(mutant['edits'], list) and mutant['edits'], 'empty mutation')
    changed = source
    for edit in mutant['edits']:
        require(set(edit) == {'old', 'new', 'count'}, 'invalid mutation edit')
        old, new, count = edit['old'], edit['new'], edit['count']
        require(isinstance(old, str) and isinstance(new, str) and old and old != new and type(count) is int and count > 0, 'mutation must change a nonempty exact anchor')
        require(changed.count(old) == count, f'mutation anchor count mismatch: {name}/{mutant["id"]}')
        changed = changed.replace(old, new)
    require(changed != source, 'mutation did not change model behavior')
    module = name + '_' + mutant['id']
    changed, count = re.subn(r'\bMODULE\s+' + re.escape(name) + r'\b', 'MODULE ' + module, changed)
    require(count == 1, 'mutant module rename failed')
    return module, changed


def validate_result(returncode, text, expected=None):
    if expected is None:
        require(returncode == 0 and 'No error has been found' in text, 'TLC baseline did not complete successfully')
    else:
        require(returncode == 12 and f'Invariant {expected} is violated' in text, 'mutation did not produce its required invariant counterexample')


def exact_head_inputs(root):
    """Reject dirty index/tree and undeclared executable/model inputs for merge evidence."""
    root = Path(root)
    def git(*args):
        result = subprocess.run(['git', '-C', str(root), *args], capture_output=True, check=False)
        require(result.returncode == 0, 'immutable HEAD input check failed')
        return result.stdout
    head = git('rev-parse', '--verify', 'HEAD^{commit}').decode().strip()
    git('diff', '--quiet', 'HEAD', '--')
    git('diff', '--cached', '--quiet', 'HEAD', '--')
    require(not git('ls-files', '--others', '--exclude-standard', '-z'), 'untracked files cannot be merge evidence')
    ignored = git('ls-files', '--others', '--ignored', '--exclude-standard', '-z').decode().split('\0')
    require(not any(p and (p.startswith('formal/') or p.startswith('scripts/') and not p.endswith('.md')) for p in ignored), 'ignored model/policy input cannot be merge evidence; use an external Python cache')
    tracked = git('ls-tree', '-r', '--name-only', '-z', head).decode().split('\0')
    registry = load_json(git('show', head + ':formal/models.json'))
    scope = {p for model in registry['models'] for p in model['source_scope']}
    module_inputs = {'go.mod', 'go.sum'}
    require(module_inputs <= set(tracked), 'module/checksum inputs must exist in immutable HEAD')
    paths = {p for p in tracked if p and (p.startswith(('formal/', 'scripts/', '.github/')) or p == 'Makefile' or p in scope or p in module_inputs)}
    content, hashes = {}, {}
    for path in sorted(paths):
        expected = git('show', head + ':' + path)
        actual_path = asset(root, path)
        if path in module_inputs:
            entry = git('ls-tree', head, '--', path).decode().split()
            require(entry and entry[0] == '100644', 'module input HEAD mode must be regular nonexecutable: ' + path)
            current_mode = actual_path.lstat().st_mode
            require(stat.S_ISREG(current_mode) and not current_mode & 0o111, 'module input working mode must be regular nonexecutable: ' + path)
        actual = actual_path.read_bytes()
        require(actual == expected, 'working input differs from HEAD: ' + path)
        content[path], hashes[path] = expected, digest(expected)
    return {'head': head, 'content': content, 'hashes': hashes}


def run_suite(root, jar, reports, registry_path='formal/models.json', timeout=60, merge_gate=False):
    root, reports = Path(root), Path(reports)
    reports.mkdir(parents=True, exist_ok=True)
    evidence = {'kind': 'bounded-tlc-not-go-refinement', 'success': False, 'runs': []}
    try:
        snapshot = exact_head_inputs(root) if merge_gate else None
        registry = load_registry(root, registry_path)
        if snapshot:
            require(registry['_digest'] == digest(snapshot['content'][registry_path]), 'registry changed during input verification')
            require(registry['_checksum'] == snapshot['content'][registry['tool']['checksum_file']].decode().split()[0], 'checksum changed during input verification')
            evidence.update(input_mode='immutable-head', head_sha=snapshot['head'], input_sha256=snapshot['hashes'])
        else:
            evidence['input_mode'] = 'working-tree-specification-only-not-merge-evidence'
        print('Formal input mode: ' + evidence['input_mode'])
        jar = Path(jar)
        require(jar.is_file() and jar.stat().st_size > 0, 'missing TLC jar')
        jar_bytes = jar.read_bytes()
        require(digest(jar_bytes) == registry['_checksum'], 'TLC jar checksum mismatch')
        java = shutil.which('java')
        require(java is not None, 'Java tool is unavailable')
        evidence.update(tool_version=registry['tool']['version'], tool_sha256=registry['_checksum'], registry_sha256=registry['_digest'])
        with tempfile.TemporaryDirectory(prefix='tcsim-tlc-') as directory:
            temp = Path(directory)
            pinned_jar = temp / 'tla2tools.jar'
            pinned_jar.write_bytes(jar_bytes)
            for model in registry['models']:
                source = snapshot['content'][model['model']].decode() if snapshot else asset(root, model['model']).read_text()
                config = snapshot['content'][model['config']].decode() if snapshot else asset(root, model['config']).read_text()
                cases = [(model['id'], source, None)] + [(*apply_mutant(source, model['id'], m), m['expect_invariant']) for m in model['mutants']]
                for name, candidate, expected in cases:
                    tla, cfg = temp / (name + '.tla'), temp / (name + '.cfg')
                    tla.write_text(candidate)
                    cfg.write_text(config)
                    command = [java, '-cp', str(pinned_jar), 'tlc2.TLC', '-workers', '1', '-metadir', str(temp / (name + '-states')), '-config', str(cfg), str(tla)]
                    try:
                        result = subprocess.run(command, capture_output=True, text=True, timeout=timeout, check=False, cwd=temp)
                    except (OSError, subprocess.TimeoutExpired) as error:
                        raise GateError('TLC could not finish: ' + name) from error
                    text = result.stdout + result.stderr
                    (reports / (name + '.txt')).write_text(text)
                    run = {'model': model['id'], 'case': name, 'expected_violation': expected, 'exit_code': result.returncode, 'model_sha256': digest(candidate.encode()), 'config_sha256': digest(config.encode()), 'bounds': model['bounds'], 'limits': model['limits'], 'success': False}
                    evidence['runs'].append(run)
                    validate_result(result.returncode, text, expected)
                    run['success'] = True
                    print(name + ': ' + ('bounded invariants hold' if expected is None else 'required counterexample observed'))
        if snapshot:
            after = exact_head_inputs(root)
            require(after['head'] == snapshot['head'] and after['hashes'] == snapshot['hashes'], 'HEAD/policy/model inputs changed while TLC was running')
        evidence['success'] = True
        return evidence
    except (GateError, OSError, KeyError, TypeError) as error:
        evidence['error'] = str(error)
        raise GateError(str(error)) from error
    finally:
        (reports / 'evidence.json').write_text(json.dumps(evidence, indent=2) + '\n')


def main():
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--registry', default='formal/models.json')
    parser.add_argument('--reports', default='reports/formal')
    parser.add_argument('--merge-gate', action='store_true', help='require immutable HEAD inputs before/after TLC; required in CI')
    options = parser.parse_args()
    try:
        require(bool(os.environ.get('TLA_JAR')), 'TLA_JAR must identify the pinned TLC jar')
        run_suite(Path.cwd(), os.environ['TLA_JAR'], options.reports, options.registry, merge_gate=options.merge_gate)
    except GateError as error:
        parser.exit(1, 'Formal validation failed: ' + str(error) + '\n')
