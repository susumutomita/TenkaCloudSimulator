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
    'sakura-server-metadata': {
        'argv': ['go', 'test', '-json', '-count=1', '-run', '^TestSakuraServerMetadata(AcceptanceCRUD|AcceptanceRefusals|ComparatorControls)$', './internal/sakura'],
        'tests': ['TestSakuraServerMetadataAcceptanceCRUD',
                  'TestSakuraServerMetadataAcceptanceRefusals',
                  'TestSakuraServerMetadataComparatorControls'],
        'oracle': 'internal/sakura/server_metadata_acceptance_test.go',
        'oracle_sha256': '83b23d83b80bfb5def2104d9a6eb2fb73e1edff3f1eea0db2eeee9d292915e9a',
        'provider': 'sakura',
        'operations': ['CreateServer', 'ReadServer', 'FindServers', 'UpdateServer', 'DeleteServer'],
        'package': 'github.com/susumutomita/TenkaCloudSimulator/internal/sakura',
        'scope': 'Opt-in in-process Server metadata oracle; simulator-only 2 CPU/4096 MB; not VM, host auth, SDK or real Sakura acceptance',
    },
}


# Sakura is opt-in; retain the existing default SQS and EC2 order.
DEFAULT_SUITES = ('sqs-lifecycle', 'ec2-describe-subnets')
SAKURA_SERVER_PATHS = frozenset({
    'internal/sakura/iaas/server_store.go',
    'internal/sakura/iaas/server_store_test.go',
    'internal/sakura/iaas/server_http.go',
    'internal/sakura/iaas/server_http_test.go',
})


def require(ok, reason):
    if not ok:
        raise ValueError(reason)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def git_environment():
    return {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], env=git_environment(), stderr=subprocess.PIPE).decode().strip()


def validate_scope(suites, provider, operation, paths):
    require(suites and set(suites) <= set(SUITES), 'registered acceptance suites required')
    matching = {suite for suite in suites if SUITES[suite]['provider'] == provider and operation in SUITES[suite]['operations']}
    require(matching, 'acceptance suite does not cover this provider/operation')
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
        if 'sakura-server-metadata' in matching:
            require(path in SAKURA_SERVER_PATHS, 'Sakura Server pilot does not cover claimed path: ' + path)


def candidate_paths(base, head, root=ROOT):
    require(all(isinstance(s, str) and re.fullmatch(r'[0-9a-f]{40}', s) for s in (base, head)), 'immutable diff refs required')
    require(git(root, 'rev-parse', base + '^{commit}') == base and git(root, 'rev-parse', head + '^{commit}') == head, 'invalid diff refs')
    require(git(root, 'merge-base', base, head) == base, 'candidate must descend from claimed base')
    return sorted(p for p in git(root, 'diff', '--no-renames', '--no-ext-diff', '--name-only', '-z', base, head, '--').split('\0') if p)


def clean_head(root):
    require(not git(root, 'status', '--porcelain', '--untracked-files=all'), 'commit all inputs before acceptance; dirty/untracked work is not exact HEAD')
    return git(root, 'rev-parse', 'HEAD')


def source_hash(root):
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
        if isinstance(oracle, str):
            oracle_hash(root, suite)
        elif oracle is not None:
            name, pinned = oracle
            path = root / name
            require(path.is_file() and not path.is_symlink() and digest(path.read_bytes()) == pinned,
                    'registered suite oracle changed or missing: ' + name)


def verdict(log, suite, returncode):
    require(returncode == 0, 'acceptance process failed or timed out')
    events = [json.loads(line) for line in log.decode().splitlines() if line.strip()]
    require(events, 'no test events')
    require(not any(e.get('Action') in ('skip', 'fail') for e in events), 'failed or skipped tests are not acceptance')
    spec = SUITES[suite]
    package = spec['package']
    tests = spec['tests'] if 'tests' in spec else [spec['test']]
    for test in tests:
        runs = [e for e in events if e.get('Package') == package and e.get('Test') == test and e.get('Action') == 'run']
        passes = [e for e in events if e.get('Package') == package and e.get('Test') == test and e.get('Action') == 'pass']
        require(len(runs) == 1 and len(passes) == 1, 'required test did not execute exactly once: ' + test)
    require(any(e.get('Package') == package and not e.get('Test') and e.get('Action') == 'pass' for e in events), 'package completion missing')


def oracle_hash(root, suite):
    spec = SUITES[suite]
    if 'oracle_sha256' not in spec:
        return None
    path = root / spec['oracle']
    require(not path.is_symlink(), 'oracle must not be a symlink')
    value = digest(path.read_bytes())
    require(value == spec['oracle_sha256'], 'reviewed suite oracle changed')
    return value


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
    if command[0] == 'go':
        requested = os.environ.get('GO', 'go')
        require(requested and not any(c in requested for c in ('\n', '\r', '\x00')), 'GO must be one executable path, not a shell command')
        executable = shutil.which(requested)
        require(executable, 'configured Go executable unavailable')
        command[0] = str(Path(executable).resolve())
    return command


def run(root, output, suites, timeout=300):
    require(suites and len(suites) == len(set(suites)) and set(suites) <= set(SUITES), 'unknown/duplicate/empty acceptance suites')
    head = clean_head(root)
    verify_suite_oracles(root, suites)
    pin = source_hash(root)
    report = {'version': 1, 'head': head, 'source_sha256': pin, 'checks': {}, 'status': 'failed'}
    output = output.resolve()
    # Output must not overwrite tracked source. Repository-local reports must be ignored.
    if output.is_relative_to(root.resolve()):
        rel = str(output.relative_to(root.resolve()))
        require(subprocess.run(['git','-C',str(root),'check-ignore','-q',rel], env=git_environment()).returncode == 0, 'report output must be outside repository or ignored')
    output.parent.mkdir(parents=True, exist_ok=True)
    try:
        for suite in suites:
            oracle = oracle_hash(root, suite)
            command = SUITES[suite]['argv']
            effective_command = executable_command(command)
            env = execution_environment()
            with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
                process = subprocess.Popen(effective_command, cwd=root, env=env, stdout=stdout, stderr=stderr, start_new_session=True)
                try:
                    returncode = process.wait(timeout=timeout)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
                    returncode = -1
                stdout.seek(0); log = stdout.read()
                stderr.seek(0); errors = stderr.read()
            # Local logs may contain test data; never publish them automatically.
            logpath = output.with_name(output.stem + '-' + suite + '.jsonl')
            errpath = output.with_name(output.stem + '-' + suite + '.stderr')
            logpath.write_bytes(log); errpath.write_bytes(errors)
            entry = {'status':'failed', 'argv':command, 'executed_argv':effective_command, 'returncode':returncode, 'log':logpath.name,
                     'log_sha256':digest(log), 'scope':SUITES[suite]['scope']}
            if oracle is not None:
                entry['oracle_sha256'] = oracle
            report['checks'][suite] = entry
            verdict(log, suite, returncode)
            entry['status'] = 'passed'
        for suite in suites:
            oracle_hash(root, suite)
        require(clean_head(root) == head and source_hash(root) == pin, 'inputs changed during acceptance')
        report['status'] = 'passed'
    finally:
        output.write_text(json.dumps(report, indent=2) + '\n')
    return report


def verify(path, head, suites, root=ROOT):
    require(suites and len(suites) == len(set(suites)) and set(suites) <= set(SUITES), 'registered acceptance suites required')
    path = Path(path)
    report = json.loads(path.read_text())
    require(report.get('version') == 1 and report.get('head') == head and report.get('status') == 'passed', 'acceptance is not passed at candidate HEAD')
    require(clean_head(root) == head, 'review checkout is not clean candidate HEAD')
    require(report.get('source_sha256') == source_hash(root), 'acceptance oracle changed')
    verify_suite_oracles(root, suites)
    require(set(report.get('checks', {})) == set(suites), 'acceptance suite set differs')
    for suite in suites:
        entry = report['checks'][suite]
        oracle = oracle_hash(root, suite)
        if oracle is not None:
            require(entry.get('oracle_sha256') == oracle, 'suite oracle receipt differs')
        require(entry.get('status') == 'passed' and entry.get('argv') == SUITES[suite]['argv'], 'acceptance command/status differs')
        require(entry.get('executed_argv') == executable_command(SUITES[suite]['argv']), 'acceptance toolchain command differs; rerun on reviewer executor')
        name = entry['log']
        require(isinstance(name,str) and Path(name).name == name and name not in ('','.','..'), 'unsafe evidence path')
        log = (path.parent / name).read_bytes()
        require(digest(log) == entry['log_sha256'], 'acceptance log changed')
        verdict(log, suite, entry['returncode'])
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
    print('factory acceptance passed; scope: Go gateway/SQLite only')

if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.SubprocessError) as exc:
        print('factory acceptance blocked: ' + str(exc), file=sys.stderr)
        sys.exit(1)
