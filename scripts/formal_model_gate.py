"""Fail-closed finite TLC validation; no implementation/refinement proof."""
import hashlib
import json
import os
import re
import shutil
import subprocess
import tempfile
from pathlib import Path

BASE_MODELS = {'ObjectStore', 'StackOwnership', 'S3Versions', 'NativeIAMOwnership', 'RESTDeploymentSnapshot'}
IDENTIFIER = re.compile(r'^[A-Za-z][A-Za-z0-9_]*$')


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
    names = []
    collecting = False
    for raw in config.splitlines():
        line = raw.split('\\*', 1)[0].strip()
        if not line:
            continue
        words = line.split()
        if words[0] in ('INVARIANT', 'INVARIANTS'):
            collecting = True
            names.extend(words[1:])
        elif words[0] in ('CONSTANT', 'CONSTANTS', 'INIT', 'NEXT', 'SPECIFICATION', 'CHECK_DEADLOCK', 'PROPERTY', 'PROPERTIES', 'CONSTRAINT', 'SYMMETRY'):
            collecting = False
        elif collecting:
            names.extend(words)
    require(names and len(names) == len(set(names)) and all(IDENTIFIER.fullmatch(n) for n in names), 'invalid or absent config invariants')
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


def run_suite(root, jar, reports, registry_path='formal/models.json', timeout=60):
    root, reports = Path(root), Path(reports)
    reports.mkdir(parents=True, exist_ok=True)
    evidence = {'kind': 'bounded-tlc-not-go-refinement', 'success': False, 'runs': []}
    try:
        registry = load_registry(root, registry_path)
        jar = Path(jar)
        require(jar.is_file() and jar.stat().st_size > 0, 'missing TLC jar')
        require(digest(jar.read_bytes()) == registry['_checksum'], 'TLC jar checksum mismatch')
        java = shutil.which('java')
        require(java is not None, 'Java tool is unavailable')
        evidence.update(tool_version=registry['tool']['version'], tool_sha256=registry['_checksum'], registry_sha256=registry['_digest'])
        with tempfile.TemporaryDirectory(prefix='tcsim-tlc-') as directory:
            temp = Path(directory)
            for model in registry['models']:
                source = asset(root, model['model']).read_text()
                config = asset(root, model['config']).read_text()
                cases = [(model['id'], source, None)] + [(*apply_mutant(source, model['id'], m), m['expect_invariant']) for m in model['mutants']]
                for name, candidate, expected in cases:
                    tla, cfg = temp / (name + '.tla'), temp / (name + '.cfg')
                    tla.write_text(candidate)
                    cfg.write_text(config)
                    command = [java, '-cp', str(jar.resolve()), 'tlc2.TLC', '-workers', '1', '-metadir', str(temp / (name + '-states')), '-config', str(cfg), str(tla)]
                    try:
                        result = subprocess.run(command, capture_output=True, text=True, timeout=timeout, check=False)
                    except (OSError, subprocess.TimeoutExpired) as error:
                        raise GateError('TLC could not finish: ' + name) from error
                    text = result.stdout + result.stderr
                    (reports / (name + '.txt')).write_text(text)
                    run = {'model': model['id'], 'case': name, 'expected_violation': expected, 'exit_code': result.returncode, 'model_sha256': digest(candidate.encode()), 'config_sha256': digest(config.encode()), 'bounds': model['bounds'], 'limits': model['limits'], 'success': False}
                    evidence['runs'].append(run)
                    validate_result(result.returncode, text, expected)
                    run['success'] = True
                    print(name + ': ' + ('bounded invariants hold' if expected is None else 'required counterexample observed'))
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
    options = parser.parse_args()
    try:
        require(bool(os.environ.get('TLA_JAR')), 'TLA_JAR must identify the pinned TLC jar')
        run_suite(Path.cwd(), os.environ['TLA_JAR'], options.reports, options.registry)
    except GateError as error:
        parser.exit(1, 'Formal validation failed: ' + str(error) + '\n')
