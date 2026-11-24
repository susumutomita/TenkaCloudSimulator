#!/usr/bin/env python3
"""Agent-neutral, offline acceptance executor. Reports are evidence, not authority."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
SOURCE = 'test/factory/upstream/source.json'
# Fixed reviewed commands only. Never execute command strings from findings/reports.
SUITES = {
    'sqs-lifecycle': {
        'argv': ['go', 'test', '-json', '-count=1', '-run', '^TestSQSFactoryFidelityLifecycle$', './internal/aws'],
        'test': 'TestSQSFactoryFidelityLifecycle',
        'provider': 'aws',
        'operations': ['CreateQueue', 'SendMessage', 'ReceiveMessage', 'DeleteMessage', 'DeleteQueue'],
        'package': 'github.com/susumutomita/TenkaCloudSimulator/internal/aws',
        'scope': 'Go gateway/SQLite; not SDK, UI, VM, real AWS or whole-service compatibility',
    },
    'ec2-describe-subnets': {
        'argv': ['go', 'test', '-json', '-count=1', '-run', '^TestEC2DescribeSubnetsContract$', './internal/aws'],
        'test': 'TestEC2DescribeSubnetsContract',
        'provider': 'aws',
        'operations': ['DescribeSubnets'],
        'package': 'github.com/susumutomita/TenkaCloudSimulator/internal/aws',
        'scope': 'Go gateway/SQLite CFN-owned subnet discovery; not SDK, UI, VM, real AWS or whole-service compatibility',
        'oracle': ('internal/aws/ec2_describe_subnets_contract_test.go',
                   '6443bc90b6fec1c7b8bf277cc090b00f6dd431fac3481d120fdefa0ccb09b2d9'),
    },
}


# Registration is distinct from activation. Preserve main's existing default;
# SimpleDB acceptance remains explicit-only; registration is not activation.
DEFAULT_SUITES = ('sqs-lifecycle', 'ec2-describe-subnets')
SIMPLEDB_SUITES = ('simpledb-first-oracle', 'simpledb-first-native',
                  'simpledb-first-sdk-wire', 'simpledb-first-sdk-harness-controls')
SIMPLEDB_OPERATIONS = ['CreateDomain', 'ListDomains', 'DeleteDomain', 'PutAttributes', 'GetAttributes']
SIMPLEDB_NATIVE_TESTS = ['TestSimpleDBFirstHarnessReadiness', 'TestSimpleDBFirstDomainItems',
    'TestSimpleDBFirstRestartDeletion', 'TestSimpleDBFirstListPagination',
    'TestSimpleDBFirstValidation', 'TestSimpleDBFirstDefaultPageBoundary']
SIMPLEDB_NEGATIVE_TESTS = ['TestSimpleDBFirstValidation/' + name for name in (
    'create-missing-name', 'create-short-name', 'create-space-name', 'list-zero',
    'list-over-limit', 'list-invalid-token', 'put-missing-item', 'put-missing-value',
    'put-empty-attribute', 'get-missing-domain', 'get-invalid-consistency',
    'put-partial-valid-then-missing')]
# Only explicitly reviewed leaf files; no Gateway, server, auth or shared registry.
SIMPLEDB_PATHS = frozenset('internal/aws/simpledb/' + name for name in (
    'store.go', 'domain.go', 'item.go', 'query.go', 'validation.go', 'pagination.go',
    'store_test.go', 'domain_test.go', 'item_test.go', 'query_test.go',
    'validation_test.go', 'pagination_test.go'))
SUITES.update({
 'simpledb-first-oracle': {
    'argv': ['go', 'test', '-json', '-count=1', './test/factory/testdata/simpledb/oracle'],
    'tests': ['TestSimpleDBFirstOracle'],
    'package': 'github.com/susumutomita/TenkaCloudSimulator/test/factory/testdata/simpledb/oracle',
    'provider': 'aws', 'operations': [], 'scope': 'Pure TestWriter hash integrity only; not product acceptance'},
 'simpledb-first-native': {
    'argv': ['go', 'test', '-p=1', '-json', '-count=1', '-timeout=60s', './test/factory/testdata/simpledb/native'],
    'tests': SIMPLEDB_NATIVE_TESTS + SIMPLEDB_NEGATIVE_TESTS,
    'package': 'github.com/susumutomita/TenkaCloudSimulator/test/factory/testdata/simpledb/native',
    'provider': 'aws', 'operations': SIMPLEDB_OPERATIONS,
    'scope': 'First five native SimpleDB APIs/SQLite; not SDK, UI, Select, conditional writes or whole service'},
 'simpledb-first-sdk-wire': {
    'argv': ['python3', '-B', 'scripts/test-simpledb-factory-sdk.py'], 'format': 'simpledb-sdk-v1',
    'provider': 'aws', 'operations': SIMPLEDB_OPERATIONS,
    'scope': 'Fixed local boto3 + freshly built same-HEAD CLI; no real AWS or new signature authority'},
 'simpledb-first-sdk-harness-controls': {
    'argv': ['python3', '-B', 'test/factory/testdata/simpledb/test_sdk_harness.py', '-v'],
    'format': 'python-unittest', 'tests': [
       'test_missing_attribute_fields_are_product_failure',
       'test_cleanup_failure_preserves_primary_product_failure',
       'test_sdk_origin_guard_rejects_any_other_origin'],
    'provider': 'aws', 'operations': [], 'scope': 'Pure SDK harness controls; no SDK/product execution'},
})


def require(ok, reason):
    if not ok:
        raise ValueError(reason)


def require_no_mixed_suites(suites):
    selected = set(suites)
    require(not selected.intersection(SIMPLEDB_SUITES) or selected <= set(SIMPLEDB_SUITES),
            'SimpleDB suites cannot be mixed with other pilots')


def digest(data):
    return hashlib.sha256(data).hexdigest()


def git_environment():
    return {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], env=git_environment(), stderr=subprocess.PIPE).decode().strip()


def validate_scope(suites, provider, operation, paths):
    require(suites and set(suites) <= set(SUITES), 'registered acceptance suites required')
    require_no_mixed_suites(suites)
    matching = {suite for suite in suites if SUITES[suite]['provider'] == provider and operation in SUITES[suite]['operations']}
    require(matching, 'acceptance suite does not cover this provider/operation')
    if matching.intersection(SIMPLEDB_SUITES):
        require(set(suites) == set(SIMPLEDB_SUITES) and len(suites) == len(SIMPLEDB_SUITES),
                'all independent SimpleDB surfaces required, including SDK')
        require(isinstance(paths, list) and paths and len(paths) == len(set(paths)),
                'nonempty unique ownership paths required')
        for path in paths:
            require(path in SIMPLEDB_PATHS, 'SimpleDB pilot does not cover claimed path: ' + path)
        return
    for path in paths:
        p = Path(path)
        sqs_covered = path in ('internal/aws/sqs.go', 'internal/aws/sqs_store.go', 'formal/applicability.json') or (
            p.parent.as_posix() == 'internal/aws' and p.name.startswith('sqs_') and p.name.endswith('_test.go'))
        subnet_covered = path in (
            'internal/aws/ec2_read.go', 'internal/aws/ec2_vpc.go',
            'internal/aws/ec2_describe_subnets_contract_test.go',
            'formal/applicability.json', 'scripts/factory_acceptance.py', 'scripts/test-factory-acceptance.py',
        )
        if 'sqs-lifecycle' in matching:
            require(sqs_covered, 'SQS pilot does not cover claimed path: ' + path)
        if 'ec2-describe-subnets' in matching:
            require(subnet_covered, 'DescribeSubnets pilot does not cover claimed path: ' + path)


def candidate_paths(base, head, root=ROOT):
    require(all(isinstance(s, str) and re.fullmatch(r'[0-9a-f]{40}', s) for s in (base, head)), 'immutable diff refs required')
    require(git(root, 'rev-parse', base + '^{commit}') == base and git(root, 'rev-parse', head + '^{commit}') == head, 'invalid diff refs')
    require(git(root, 'merge-base', base, head) == base, 'candidate must descend from claimed base')
    return sorted(p for p in git(root, 'diff', '--no-renames', '--no-ext-diff', '--name-only', '-z', base, head, '--').split('\0') if p)


def clean_head(root):
    require(not git(root, 'status', '--porcelain', '--untracked-files=all'), 'commit all inputs before acceptance; dirty/untracked work is not exact HEAD')
    return git(root, 'rev-parse', 'HEAD')


def legacy_source_hash(root):
    raw = (root / SOURCE).read_bytes()
    source = json.loads(raw)
    require(re.fullmatch(r'[0-9a-f]{40}', source['commit']), 'upstream immutable SHA required')
    for item in source['files']:
        path = Path(item['path'])
        require(not path.is_absolute() and '..' not in path.parts, 'unsafe source path')
        data = (root / path).read_bytes()
        require(digest(data) == item['sha256'], 'upstream checksum mismatch')
        blob = hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest()
        require(blob == item['git_blob'], 'upstream Git blob mismatch')
    return digest(raw)


def verify_suite_oracles(root, suites):
    for suite in suites:
        oracle = SUITES[suite].get('oracle')
        if oracle is not None:
            name, pinned = oracle
            path = root / name
            require(path.is_file() and not path.is_symlink() and digest(path.read_bytes()) == pinned,
                    'registered suite oracle changed or missing: ' + name)


def source_hash(root, suites=None):
    selected = set(suites or DEFAULT_SUITES)
    require_no_mixed_suites(selected)
    if not selected.intersection(SIMPLEDB_SUITES):
        return legacy_source_hash(root)  # Preserve legacy receipts and SQS provenance.
    registry = root/'test/factory/simpledb-registration.json'
    raw = registry.read_bytes()
    data = json.loads(raw)
    require(data.get('test_writer_commit') == '7253c5b5dd9b0ba4574fbfe8bb2209228f99ba6d', 'wrong TestWriter snapshot')
    expected = set('test/factory/testdata/simpledb/' + n for n in (
        'README.md', 'files.sha256.json', 'native/acceptance_test.go', 'oracle/fixture_test.go',
        'oracle/fixtures.json', 'oracle/oracle.json', 'registration-proposal.json',
        'sdk_wire.py', 'test_sdk_harness.py')) | {'scripts/test-simpledb-factory-sdk.py'}
    require(set(data.get('files', {})) == expected, 'SimpleDB oracle/helper set differs')
    for name, checksum in data['files'].items():
        file = root/name
        require(not file.is_symlink() and file.resolve().is_relative_to(root.resolve()), 'unsafe SimpleDB input')
        require(digest(file.read_bytes()) == checksum, 'SimpleDB oracle/helper changed: ' + name)
    binding = {'registration_sha256': digest(raw), 'suites': {name: SUITES[name] for name in SIMPLEDB_SUITES},
               'paths': sorted(SIMPLEDB_PATHS)}
    return digest(json.dumps(binding, sort_keys=True).encode())


def verdict(log, suite, returncode, head=None):
    require(returncode == 0, 'acceptance process failed or timed out')
    spec = SUITES[suite]
    if spec.get('format') == 'simpledb-sdk-v1':
        result = json.loads(log.decode())
        require(head and result.get('factory_sdk_v1') is True and result.get('head') == head, 'SDK receipt HEAD missing/different')
        require(result.get('status') == 'PASS' and not result.get('cleanup_errors'), 'SDK product/harness/cleanup did not pass')
        require(result.get('passed_cases') == ['sdk-domain-items', 'sdk-restart-cascade', 'sdk-list-pages', 'sdk-negative-cases'], 'required SDK cases missing/duplicate')
        require(result.get('sdk_pins') == {'boto3': '1.42.0', 'botocore': '1.41.6'}, 'SDK pins differ')
        require(re.fullmatch('[0-9a-f]{64}', result.get('binary_sha256', '')), 'built CLI hash required')
        require(result.get('python') == executable_command(['python3'])[0] and result.get('go') == executable_command(['go'])[0], 'SDK toolchain differs')
        return
    if spec.get('format') == 'python-unittest':
        lines = log.decode().splitlines()
        for name in spec['tests']:
            require(sum(bool(re.fullmatch(re.escape(name) + r' \(.*\) \.\.\. ok', line)) for line in lines) == 1, 'required pure harness test missing/duplicate')
        require(lines.count('OK') == 1 and sum(bool(re.fullmatch(r'Ran 3 tests in [0-9.]+s', line)) for line in lines) == 1, 'pure harness completion missing')
        require(not any(word in log.decode() for word in ('skipped', 'FAILED', 'ERROR', 'expected failure', 'unexpected success')), 'pure harness skip/failure refused')
        return
    events = [json.loads(line) for line in log.decode().splitlines() if line.strip()]
    require(events, 'no test events')
    require(not any(e.get('Action') in ('skip', 'fail', 'build-fail') for e in events), 'failed or skipped tests are not acceptance')
    package = spec['package']
    for test in spec.get('tests', [spec.get('test')]):
        runs = [e for e in events if e.get('Package') == package and e.get('Test') == test and e.get('Action') == 'run']
        passes = [e for e in events if e.get('Package') == package and e.get('Test') == test and e.get('Action') == 'pass']
        require(len(runs) == 1 and len(passes) == 1, 'required test did not execute exactly once: ' + str(test))
    require(any(e.get('Package') == package and not e.get('Test') and e.get('Action') == 'pass' for e in events), 'package completion missing')


def execution_environment():
    # A build overlay / alternate workspace can make a clean Git HEAD test other
    # code. Ignore ambient Go configuration as well as cloud/profile/proxy vars.
    env = {k: v for k, v in os.environ.items()
           if k in ('PATH', 'HOME', 'TMPDIR', 'GOCACHE', 'GOMODCACHE', 'GOPATH', 'GOROOT', 'SYSTEMROOT')}
    env.update(GOENV='off', GOWORK='off', GOFLAGS='', GOTOOLCHAIN='local',
               GOPROXY='off', GOSUMDB='off', AWS_EC2_METADATA_DISABLED='true',
               AWS_CONFIG_FILE=os.devnull, AWS_SHARED_CREDENTIALS_FILE=os.devnull)
    return env


def executable_command(command):
    command = list(command)
    if command[0] in ('go', 'python3'):
        requested = os.environ.get('GO', 'go') if command[0] == 'go' else os.environ.get('FACTORY_SDK_PYTHON', sys.executable)
        require(requested and not any(c in requested for c in ('\n', '\r', '\x00')), 'tool must be one executable path, not a shell command')
        executable = shutil.which(requested)
        require(executable, 'configured Go executable unavailable')
        command[0] = str(Path(executable).parent.resolve()/Path(executable).name) if command[0] == 'python3' else str(Path(executable).resolve())
    return command


def run(root, output, suites, timeout=300):
    require(suites and len(suites) == len(set(suites)) and set(suites) <= set(SUITES), 'unknown/duplicate/empty acceptance suites')
    require_no_mixed_suites(suites)
    head = clean_head(root)
    verify_suite_oracles(root, suites)
    pin = source_hash(root, suites)
    report = {'version': 1, 'head': head, 'source_sha256': pin, 'checks': {}, 'status': 'failed'}
    output = output.resolve()
    # Output must not overwrite tracked source. Repository-local reports must be ignored.
    if output.is_relative_to(root.resolve()):
        rel = str(output.relative_to(root.resolve()))
        require(subprocess.run(['git','-C',str(root),'check-ignore','-q',rel], env=git_environment()).returncode == 0, 'report output must be outside repository or ignored')
    output.parent.mkdir(parents=True, exist_ok=True)
    try:
        for suite in suites:
            command = SUITES[suite]['argv']
            effective_command = executable_command(command)
            env = execution_environment()
            if suite == 'simpledb-first-sdk-wire':
                env.update(FACTORY_HEAD=head, FACTORY_GO=executable_command(['go'])[0], FACTORY_PYTHON=effective_command[0], CGO_ENABLED='0')
            with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
                process = subprocess.Popen(effective_command, cwd=root, env=env, stdout=stdout, stderr=stderr, start_new_session=True)
                try:
                    returncode = process.wait(timeout=timeout)
                except subprocess.TimeoutExpired:
                    if suite == 'simpledb-first-sdk-wire':
                        os.killpg(process.pid, signal.SIGTERM)
                        try:
                            process.wait(timeout=10)  # Allow SDK's finally to stop its owned CLI.
                        except subprocess.TimeoutExpired:
                            os.killpg(process.pid, signal.SIGKILL)
                            process.wait()
                    else:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait()
                    returncode = -1
                stdout.seek(0); log = stdout.read()
                stderr.seek(0); errors = stderr.read()
                if SUITES[suite].get('format') == 'python-unittest':
                    log += errors  # unittest writes its named events to stderr.
            # Local logs may contain test data; never publish them automatically.
            logpath = output.with_name(output.stem + '-' + suite + '.jsonl')
            errpath = output.with_name(output.stem + '-' + suite + '.stderr')
            logpath.write_bytes(log); errpath.write_bytes(errors)
            entry = {'status':'failed', 'argv':command, 'executed_argv':effective_command, 'returncode':returncode, 'log':logpath.name,
                     'log_sha256':digest(log), 'scope':SUITES[suite]['scope']}
            report['checks'][suite] = entry
            verdict(log, suite, returncode, head)
            entry['status'] = 'passed'
        verify_suite_oracles(root, suites)
        require(clean_head(root) == head and source_hash(root, suites) == pin, 'inputs changed during acceptance')
        report['status'] = 'passed'
    finally:
        output.write_text(json.dumps(report, indent=2) + '\n')
    return report


def verify(path, head, suites, root=ROOT):
    require(suites and len(suites) == len(set(suites)) and set(suites) <= set(SUITES), 'registered acceptance suites required')
    require_no_mixed_suites(suites)
    path = Path(path)
    report = json.loads(path.read_text())
    require(report.get('version') == 1 and report.get('head') == head and report.get('status') == 'passed', 'acceptance is not passed at candidate HEAD')
    require(clean_head(root) == head, 'review checkout is not clean candidate HEAD')
    require(report.get('source_sha256') == source_hash(root, suites), 'acceptance oracle changed')
    verify_suite_oracles(root, suites)
    require(set(report.get('checks', {})) == set(suites), 'acceptance suite set differs')
    for suite in suites:
        entry = report['checks'][suite]
        require(entry.get('status') == 'passed' and entry.get('argv') == SUITES[suite]['argv'], 'acceptance command/status differs')
        require(entry.get('executed_argv') == executable_command(SUITES[suite]['argv']), 'acceptance toolchain command differs; rerun on reviewer executor')
        name = entry['log']
        require(isinstance(name,str) and Path(name).name == name and name not in ('','.','..'), 'unsafe evidence path')
        log = (path.parent / name).read_bytes()
        require(digest(log) == entry['log_sha256'], 'acceptance log changed')
        verdict(log, suite, entry['returncode'], head)
    return report


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output', type=Path, default=ROOT/'reports/factory/acceptance.json')
    p.add_argument('--suite', choices=sorted(SUITES), action='append')
    p.add_argument('--verify', action='store_true')
    args = p.parse_args()
    suites = args.suite or list(DEFAULT_SUITES)
    if args.verify:
        verify(args.output, clean_head(ROOT), suites)
    else:
        run(ROOT, args.output, suites)
    print('factory checks passed; suites: ' + ', '.join(suites))

if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.SubprocessError) as exc:
        print('factory acceptance blocked: ' + str(exc), file=sys.stderr)
        sys.exit(1)
