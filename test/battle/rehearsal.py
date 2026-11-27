#!/usr/bin/env python3
"""Local rehearsal driver: unchanged hello-world-battle on the Simulator's real VM runtime.

This script stands in for the TenkaCloud host only for the steps the host does not
implement for the Simulator yet (TenkaCloud keeps this Battle gated off in
simulator mode). It uses the Simulator's actual authenticated HTTP surfaces:

  initialize : native SDK CloudFormation CreateStack with the unchanged template
  operate    : participant AssumeRole + Session Manager terminal, endpoint
               registration, operator AWS-RunShellScript disruption
  grade      : owned HTTP probes through the Battle capability endpoints; points
               are computed HERE from the catalog's scoring metadata (the
               Simulator never calculates points)
  cleanup    : capability seal, DeleteStack, process/socket/disk checks, SIGTERM

No real AWS endpoint is contacted: every client is pinned to numeric loopback.
Secrets (operator token, STS keys, session tokens) are never written to the report.
See README.md in this directory for the bundle this needs and what a pass means.
"""
import argparse
import base64
from contextlib import contextmanager
import hashlib
import json
import math
import os
import pathlib
import re
import secrets
import shutil
import signal
import socket
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from xml.etree import ElementTree

os.environ.update(AWS_CONFIG_FILE='/dev/null', AWS_SHARED_CREDENTIALS_FILE='/dev/null', AWS_EC2_METADATA_DISABLED='true')
for name in ('HTTP_PROXY', 'HTTPS_PROXY', 'http_proxy', 'https_proxy', 'ALL_PROXY', 'all_proxy'):
    os.environ.pop(name, None)
import boto3  # noqa: E402
from botocore.auth import SigV4Auth  # noqa: E402
from botocore.awsrequest import AWSRequest  # noqa: E402
from botocore.config import Config  # noqa: E402
from botocore.credentials import Credentials  # noqa: E402
from botocore.exceptions import (  # noqa: E402
    ClientError, ConnectionClosedError, ConnectTimeoutError,
    EndpointConnectionError, HTTPClientError, ReadTimeoutError,
)
from botocore.parsers import ResponseParserError  # noqa: E402

parser = argparse.ArgumentParser()
parser.add_argument('--binary', required=True, help='simulator executable built from the commit under test')
parser.add_argument('--bundle', required=True, help='directory holding manifest.json and guest-bundle.tar.gz from pack_bundle.py')
parser.add_argument('--challenge-root', required=True, help='TenkaCloudChallenge checkout (unchanged problem source)')
parser.add_argument('--work', required=True, help='new private directory for this run (state, token, runtime files, report)')
parser.add_argument('--emulator', default='bin/qemu-system-x86_64')
parser.add_argument('--firmware', default='firmware/bios-256k.bin')
parser.add_argument('--base-disk', default='images/al2023-battle.qcow2')
parser.add_argument('--region', default='ap-northeast-1')
parser.add_argument('--probe-interval', type=float, default=60.0)
parser.add_argument('--create-timeout', type=float, default=900.0)
parser.add_argument('--userdata-timeout', type=float, default=1500.0)
parser.add_argument('--stop-after', default='')
parser.add_argument('--console-form-smoke', action='store_true',
                    help='run only the separate guarded EC2 console wire launch and physical cleanup witness')
args = parser.parse_args()

REGION = args.region
WORK = pathlib.Path(args.work).resolve()
WORK.mkdir(parents=True, exist_ok=False)
os.chmod(WORK, 0o700)
REPORT = WORK / 'report.json'
RUNTIME_DIR = WORK / 'rt'  # keep this path short: guest control sockets live below it
RUNTIME_DIR.mkdir(mode=0o700)
BUNDLE = pathlib.Path(args.bundle).resolve()
MANIFEST = (BUNDLE / 'manifest.json').read_bytes()
DISK = [f for f in json.loads(MANIFEST)['files'] if f['path'] == args.base_disk][0]
NOW_MS = int(time.time() * 1000)
# Same-host wall clock for both the driver and the Simulator. This is a rehearsal
# convenience, not the independent Host clock qualification a real event needs.
RUNTIME = {
    'bundleManifest': str(BUNDLE / 'manifest.json'), 'bundleArchive': str(BUNDLE / 'guest-bundle.tar.gz'),
    'manifestSha256': hashlib.sha256(MANIFEST).hexdigest(), 'sessionImageSha256': DISK['sha256'], 'runtimeDirectory': str(RUNTIME_DIR),
    'layout': {'emulator': args.emulator, 'firmware': args.firmware, 'baseDisk': args.base_disk},
    'imageIds': ['ami-0a1b2c3d4e5f60718'], 'lifetimeUntilMs': NOW_MS + 3 * 3600 * 1000,
    'clock': {'domain': 'rehearsal-host-wallclock-1', 'validFromMs': NOW_MS - 1000, 'validUntilMs': NOW_MS + 3 * 3600 * 1000},
}
RUNTIME_CONFIG = WORK / 'runtime.json'
with os.fdopen(os.open(RUNTIME_CONFIG, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as config:
    json.dump(RUNTIME, config, indent=1)
BATTLE = pathlib.Path(args.challenge_root) / 'battles' / 'hello-world-battle'
TEMPLATE = (BATTLE / 'template.yaml').read_bytes()
METADATA = json.loads((BATTLE / 'metadata.json').read_text())
SCORING = METADATA['scoring']
T0 = time.time()
report = {
    'boundary': 'Local rehearsal driver standing in for the host; real Simulator binary, real QEMU TCG guest, unchanged catalog template. Not TenkaCloud host/Portal acceptance and not real AWS.',
    'templateSha256': hashlib.sha256(TEMPLATE).hexdigest(),
    'metadataSha256': hashlib.sha256((BATTLE / 'metadata.json').read_bytes()).hexdigest(),
    'scoring': SCORING,
    'bundle': {'manifestSha256': RUNTIME['manifestSha256'], 'baseDiskSha256': DISK['sha256']},
    'phases': [],
    'score': {'total': 0, 'rounds': []},
}
if args.console_form_smoke:
    report.pop('templateSha256')
    report.pop('metadataSha256')
    report['runKind'] = 'ec2-console-form'


def stamp():
    return time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())


PUBLIC_PHASES = ('startup', 'console-form', 'initialize', 'operate', 'grade', 'cleanup', 'shutdown')
PUBLIC_CHECKS = (
    'battle-capability-receipt', 'battle-runtime-configured', 'capability-sealed-and-joined',
    'create-stack-response', 'disruption-stops-frontend-and-penalizes', 'disruption-operator-role-removed',
    'console-form-browser-session', 'console-form-real-qemu-process',
    'console-form-sdk-state-matches', 'console-form-stack-process-and-state-cleanup',
    'console-form-wire-succeeded',
    'ec2-inventory-empty-before-stack', 'ec2-inventory-restored-after-stack-delete',
    'ec2-readback-matches-completed-stack-instance', 'ec2-readback-requires-local-token',
    'guest-api-answers-before-terminal', 'guest-process-exited', 'healthy-round-awards-points',
    'imds-public-hostname-in-login-banner', 'native-operator-identity',
    'no-points-before-registration', 'no-vm-before-deployment', 'one-real-qemu-guest-process',
    'operator-disruption-run-shell-script', 'operator-revert-run-shell-script',
    'operator-second-disruption', 'original-userdata-finished', 'outputs-default-endpoints-empty',
    'participant-assumed-original-viewer-role', 'participant-recovers-with-systemctl-start-nginx',
    'participant-registered-both-endpoints', 'participant-session-manager-shell-ready',
    'participant-shell-executes-in-guest', 'probe-refused-after-seal',
    'recovery-restores-points', 'revert-restores-points', 'score-ledger-consistent',
    'second-disruption-penalizes', 'second-minute-awards-points',
    'simulator-exit-and-no-guest-left', 'simulator-ready-with-pinned-bundle',
    'stack-create-complete', 'stack-deleted', 'stack-iam-roles-removed',
    'userdata-installed-and-started-services', 'watching-participant-stays-attached-past-a-minute',
)
PUBLIC_ROUNDS = ('before-registration', 'healthy-1', 'healthy-2', 'after-disruption',
                 'after-participant-recovery', 'after-second-disruption', 'after-operator-revert')
PUBLIC_FAILURES = ('RuntimeError', 'TimeoutError', 'ConnectionError', 'OSError',
                   'ClientError', 'KeyboardInterrupt', 'ValueError', 'TypeError', 'UnexpectedError')
PUBLIC_STATUSES = ('CREATE_IN_PROGRESS', 'CREATE_COMPLETE', 'CREATE_FAILED',
                   'ROLLBACK_IN_PROGRESS', 'ROLLBACK_COMPLETE', 'ROLLBACK_FAILED',
                   'DELETE_IN_PROGRESS', 'DELETE_COMPLETE', 'DELETE_FAILED',
                   'GONE', 'UNKNOWN', 'Success', 'Failed', 'TimedOut', 'Cancelled')
STACK_SERVICE_OUTCOMES = (
    ('ValidationError', 400, 'service-validation-error'),
    ('AccessDenied', 403, 'service-access-denied'),
    ('InternalFailure', 500, 'service-internal-failure'),
    ('NotImplemented', 501, 'service-not-implemented'),
)
STACK_TRANSPORT_OUTCOMES = ('connection-closed', 'connect-timeout', 'read-timeout',
                            'endpoint-unreachable', 'http-client-error', 'response-decode-error')
PUBLIC_STACK_ERROR_OUTCOMES = ('other-service-error', 'other-error')
PUBLIC_STACK_CREATE_OUTCOMES = {
    'createStack': ('response-confirmed', 'malformed-service-error'),
    'describeStacks': ('stack-observed', 'no-stack-observed', 'malformed-service-error'),
    'describeStackEvents': ('instance-create-failed', 'stack-create-failed',
                            'other-resource-create-failed', 'no-failed-event-observed', 'not-attempted',
                            'malformed-event-page'),
    'simulatorAtCreateReply': ('running', 'exited'),
    'simulatorAtReadback': ('running', 'exited'),
}


def stack_request_error(error, *required_fields):
    if isinstance(error, ClientError):
        response = error.response
        # Maintain the driver's original fail-closed SDK shape checks, without
        # retaining or formatting any of the required error fields.
        for field in required_fields:
            _ = response['Error'][field]
        if type(response) is dict:
            detail, metadata = response.get('Error'), response.get('ResponseMetadata')
            code = detail.get('Code') if type(detail) is dict else None
            status = metadata.get('HTTPStatusCode') if type(metadata) is dict else None
            for exact_code, exact_status, outcome in STACK_SERVICE_OUTCOMES:
                if type(code) is str and code == exact_code and type(status) is int and status == exact_status:
                    return outcome
        return 'other-service-error'
    classes = (ConnectionClosedError, ConnectTimeoutError, ReadTimeoutError,
               EndpointConnectionError, HTTPClientError, ResponseParserError)
    for error_class, outcome in zip(classes, STACK_TRANSPORT_OUTCOMES):
        if isinstance(error, error_class):
            return outcome
    return 'other-error'


def stack_event_observation(event_page, stack_id):
    if not stack_id:
        return 'not-attempted'
    # Evidence for one stack generation only. Absence on this page is not
    # a claim that no resource failed. Never expose IDs, reasons or payloads.
    kinds = set()
    for event in event_page.get('StackEvents', []):
        # Preserve the old .get() requirement on every returned event.
        kind = event.get('ResourceType')
        if (type(kind) is str and event.get('StackId') == stack_id
                and event.get('ResourceStatus') == 'CREATE_FAILED'):
            kinds.add(kind)
    if 'AWS::EC2::Instance' in kinds:
        return 'instance-create-failed'
    if kinds - {'AWS::CloudFormation::Stack'}:
        return 'other-resource-create-failed'
    if 'AWS::CloudFormation::Stack' in kinds:
        return 'stack-create-failed'
    return 'no-failed-event-observed'


def public_stack_create_outcomes(value):
    if type(value) is not dict:
        return {}
    errors = (tuple(item[2] for item in STACK_SERVICE_OUTCOMES)
              + STACK_TRANSPORT_OUTCOMES + PUBLIC_STACK_ERROR_OUTCOMES)
    out = {}
    for source, allowed in PUBLIC_STACK_CREATE_OUTCOMES.items():
        observed = value.get(source)
        if type(observed) is str and (observed in allowed or
                source in ('createStack', 'describeStacks', 'describeStackEvents') and observed in errors):
            out[source] = observed
    return out


def simulator_process_state():
    return 'running' if process.poll() is None else 'exited'


def public_number(value, lower, upper, integer=False):
    # Do not coerce unknown objects, booleans, nonfinite floats or arbitrary text.
    return (type(value) is int or not integer and type(value) is float) and lower <= value <= upper and math.isfinite(value)


def public_details(value):
    out = {}
    if type(value) is not dict:
        return out
    for key, lower, upper, integer in (
            ('exitCode', -255, 255, True), ('responseCode', -255, 255, True),
            ('httpStatus', 100, 599, True), ('seconds', 0, 86400, False),
            ('startupSeconds', 0, 86400, False), ('latencyMs', 0, 86400000, False),
            ('points', -1000000000, 1000000000, True), ('total', -1000000000, 1000000000, True),
            ('linesSeen', 0, 1000000, True), ('pingsAnswered', 0, 1000000, True),
            ('remainingRuntimeEntries', 0, 1000000, True)):
        candidate = value.get(key)
        if public_number(candidate, lower, upper, integer):
            out[key] = candidate
    for key in ('status', 'finalStatus'):
        candidate = value.get(key)
        if type(candidate) is str and candidate in PUBLIC_STATUSES:
            out[key] = candidate
    for key in ('responded', 'settled', 'resultReady'):
        if type(value.get(key)) is bool:
            out[key] = value[key]
    return out


def public_failure(error):
    name = type(error).__name__
    return name if name in PUBLIC_FAILURES else 'UnexpectedError'


def public_report(value):
    # This is a public evidence schema, not a recursive secret-pattern filter.
    # All free-form guest/API/exception text and unknown fields stay private.
    out = {'schemaVersion': 1, 'phases': [], 'score': {'rounds': []}}
    if type(value.get('runKind')) is str and value['runKind'] == 'ec2-console-form':
        out['runKind'] = 'ec2-console-form'
    for key in ('templateSha256', 'metadataSha256'):
        digest = value.get(key)
        if type(digest) is str and re.fullmatch('[a-f0-9]{64}', digest):
            out[key] = digest
    for key, fields in (('bundle', ('manifestSha256', 'baseDiskSha256')),
                        ('simulator', ('binarySha256',))):
        source = value.get(key)
        if type(source) is dict:
            out[key] = {name: source[name] for name in fields
                        if type(source.get(name)) is str and re.fullmatch('[a-f0-9]{64}', source[name])}
    if type(value.get('result')) is str and value['result'] in ('PASS', 'PARTIAL', 'FAIL'):
        out['result'] = value['result']
    if 'failure' in value:
        failure = value['failure']
        out['failure'] = failure if type(failure) is str and failure in PUBLIC_FAILURES else 'UnexpectedError'
    phases = value.get('phases')
    for entry in phases if type(phases) is list else ():
        if type(entry) is not dict or type(entry.get('name')) is not str or entry['name'] not in PUBLIC_PHASES:
            continue
        phase_entry = {'name': entry['name'], 'checks': []}
        if entry['name'] == 'initialize':
            diagnostics = public_stack_create_outcomes(entry.get('stackCreateDiagnostics'))
            if diagnostics:
                phase_entry['stackCreateDiagnostics'] = diagnostics
        checks = entry.get('checks')
        for item in checks if type(checks) is list else ():
            if (type(item) is dict and type(item.get('name')) is str
                    and item['name'] in PUBLIC_CHECKS and type(item.get('passed')) is bool):
                phase_entry['checks'].append({'name': item['name'], 'passed': item['passed'], **public_details(item)})
        out['phases'].append(phase_entry)
    score = value.get('score')
    if type(score) is dict:
        out['score'].update(public_details({'total': score.get('total')}))
        rounds = score.get('rounds')
        for entry in rounds if type(rounds) is list else ():
            if type(entry) is not dict or type(entry.get('label')) is not str or entry['label'] not in PUBLIC_ROUNDS:
                continue
            round_entry = {'label': entry['label'], **public_details(entry), 'observations': []}
            observations = entry.get('observations')
            for item in observations if type(observations) is list else ():
                if type(item) is dict and type(item.get('slot')) is str and item['slot'] in ('frontend', 'api'):
                    round_entry['observations'].append({'slot': item['slot'], **public_details(item)})
            out['score']['rounds'].append(round_entry)
    return out


def log(message):
    # Every accepted console message is assembled only from authored constants.
    allowed = ({f'=== {name} ===' for name in PUBLIC_PHASES}
               | {f'{status} {name}' for status in ('PASS', 'FAIL') for name in PUBLIC_CHECKS}
               | {f'SCORE {name}' for name in PUBLIC_ROUNDS}
               | {'FAILURE', 'SHUTDOWN', 'RESULT PASS', 'RESULT PARTIAL', 'RESULT FAIL'})
    text = message if type(message) is str and message in allowed else 'DIAGNOSTIC WITHHELD'
    print(f'[{stamp()} +{time.time() - T0:7.1f}s] {text}', flush=True)


def save():
    public = public_report(report)
    REPORT.write_text(json.dumps(public, indent=1, sort_keys=True, allow_nan=False) + '\n')
    summary = {'result': public.get('result', 'IN_PROGRESS')}
    if 'failure' in public:
        summary['failure'] = public['failure']
    (WORK / 'simulator.log').write_text(json.dumps(summary, sort_keys=True) + '\n')


def phase(name):
    entry = {'name': name, 'startedAt': stamp(), 'checks': []}
    report['phases'].append(entry)
    log(f'=== {name} ===')
    return entry


def check(entry, name, passed, **detail):
    item = {'name': name, 'passed': bool(passed), 'at': stamp(), **detail}
    entry['checks'].append(item)
    log(f"{'PASS' if passed else 'FAIL'} {name}")
    save()
    if not passed:
        raise RuntimeError(f'{name} failed')
    return item


# ---------------------------------------------------------------- simulator process
with socket.socket() as s:
    s.bind(('127.0.0.1', 0))
    PORT = s.getsockname()[1]
ENDPOINT = f'http://127.0.0.1:{PORT}'
ORIGIN = ENDPOINT
token_path = WORK / 'operator-token'
state_path = WORK / 'state.db'
# The workflow uploads simulator.log only. Never attach a raw child stream to it.
sim_log = open(WORK / 'simulator-private.log', 'xb',
               opener=lambda path, flags: os.open(path, flags, 0o600))
(WORK / 'simulator.log').write_text('{"result": "IN_PROGRESS"}\n')
process = subprocess.Popen(
    [args.binary, '--listen', f'127.0.0.1:{PORT}', '--state', str(state_path), '--token-file', str(token_path),
     '--battle-runtime-config', str(RUNTIME_CONFIG)],
    cwd=str(WORK), env={'PATH': '/nonexistent', 'CI': '1'}, stdout=sim_log, stderr=subprocess.STDOUT)
report['simulator'] = {'pid': process.pid, 'endpoint': 'numeric loopback, ephemeral port', 'binarySha256': hashlib.sha256(pathlib.Path(args.binary).read_bytes()).hexdigest()}
TOKEN = ''


def raw_http(method, path, body=None, headers=None, timeout=30):
    request = urllib.request.Request(ENDPOINT + path, method=method, data=body, headers=headers or {})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(request, timeout=timeout) as response:
            return response.status, dict(response.headers), response.read()
    except urllib.error.HTTPError as error:
        return error.code, dict(error.headers), error.read()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


def operator_json(path, value, timeout=30):
    status, _, body = raw_http('POST', path, json.dumps(value).encode(), {'X-Simulator-Token': TOKEN, 'Content-Type': 'application/json'}, timeout)
    return status, body


def client(service, credentials=None, read_timeout=60):
    creds = credentials or {'aws_access_key_id': 'TCSIMOPERATOR', 'aws_secret_access_key': TOKEN}
    return boto3.client(service, endpoint_url=ENDPOINT, region_name=REGION,
                        config=Config(retries={'max_attempts': 0}, connect_timeout=5, read_timeout=read_timeout, proxies={}), **creds)


def instance_read_client(operator_token):
    # EC2 Query uses dummy signing credentials and separately authenticated
    # simulator access. Native TCSIMOPERATOR signing covers other services.
    ec2 = client('ec2', {'aws_access_key_id': 'TCSIM_BATTLE_EC2',
                         'aws_secret_access_key': 'dummy-local-only'}, read_timeout=10)
    if operator_token is not None:
        ec2.meta.events.register('before-sign.ec2.DescribeInstances',
                                 lambda request, **_: request.headers.add_header('X-Simulator-Token', operator_token))
    ec2.meta.events.register('after-call.ec2.DescribeInstances', require_instance_response)
    return ec2


def missing_instance_token_is_denied():
    ec2 = instance_read_client(None)
    statuses = []
    ec2.meta.events.register('before-parse.ec2.DescribeInstances',
                             lambda response_dict, **_: statuses.append(response_dict['status_code']))
    try:
        ec2.describe_instances()
    except (ClientError, ResponseParserError):
        return statuses == [401]
    finally:
        ec2.close()
    return False


def require_instance_response(http_response, parsed, **_):
    if http_response.status_code >= 300:
        return  # Keep the SDK's original ClientError.
    ns = '{http://ec2.amazonaws.com/doc/2016-11-15/}'
    root = ElementTree.fromstring(http_response.content)
    request_id = root.findtext(ns + 'requestId')
    if (http_response.status_code != 200 or root.tag != ns + 'DescribeInstancesResponse'
            or not request_id or request_id != parsed.get('ResponseMetadata', {}).get('RequestId')):
        raise RuntimeError('unqualified DescribeInstances response')


def instance_inventory(ec2):
    # The bounded native Query subset takes no ID selector and omits the
    # reservation set when the committed inventory is empty.
    response = ec2.describe_instances()
    reservations = response.get('Reservations', [])
    if type(reservations) is not list or response.get('NextToken'):
        raise RuntimeError('unqualified DescribeInstances inventory')
    instances = []
    for reservation in reservations:
        items = reservation.get('Instances')
        if type(items) is not list or not items:
            raise RuntimeError('unqualified DescribeInstances reservation')
        instances.extend(items)
    return instances


def matches_completed_stack_instance(events, stack_id, output_instance, instances):
    # Exact logical names from the workflow-pinned unchanged Battle template.
    # A ResourceStatus/physical ID from another stack, or a duplicate success
    # event, cannot stand in for any of these owning commitments.
    expected = {
        'Ec2': ('AWS::EC2::Instance', r'i-[0-9a-f]+'),
        'Vpc': ('AWS::EC2::VPC', r'vpc-[0-9a-f]+'),
        'PublicSubnet': ('AWS::EC2::Subnet', r'subnet-[0-9a-f]+'),
        'Sg': ('AWS::EC2::SecurityGroup', r'sg-[0-9a-f]+'),
    }
    owned = {}
    for event in events:
        logical = event.get('LogicalResourceId')
        if logical not in expected or event.get('ResourceStatus') != 'CREATE_COMPLETE':
            continue
        resource_type, pattern = expected[logical]
        physical = event.get('PhysicalResourceId', '')
        if (event.get('StackId') != stack_id or event.get('ResourceType') != resource_type
                or logical in owned or not re.fullmatch(pattern, physical)):
            return False
        owned[logical] = physical
    if owned.keys() != expected.keys() or len(instances) != 1:
        return False
    instance = instances[0]
    return (instance.get('InstanceId') == owned['Ec2'] == output_instance
            and instance.get('VpcId') == owned['Vpc']
            and instance.get('SubnetId') == owned['PublicSubnet']
            and [g.get('GroupId') for g in instance.get('SecurityGroups', [])] == [owned['Sg']]
            and instance.get('ImageId') == RUNTIME['imageIds'][0]
            and instance.get('InstanceType') == 't3.micro')


def console_form_smoke(ec2, before_instances):
    """Separate browser-wire witness on the same approved runtime."""
    p = phase('console-form')
    name = 'tc-console-instance-' + secrets.token_hex(6)
    describe = client('cloudformation', read_timeout=20)
    before_roles = sorted(role['RoleName'] for role in client('iam').list_roles()['Roles'])

    def exact_stack():
        try:
            stacks = describe.describe_stacks(StackName=name)['Stacks']
        except ClientError as error:
            detail = error.response.get('Error', {})
            status = error.response.get('ResponseMetadata', {}).get('HTTPStatusCode')
            if status == 400 and detail.get('Code') == 'ValidationError' and detail.get('Message') == 'Stack does not exist.':
                return None
            raise
        if len(stacks) != 1 or stacks[0].get('StackName') != name:
            raise RuntimeError('Console form stack lookup was unconfirmed')
        return stacks[0]

    if exact_stack() is not None:
        raise RuntimeError('Console smoke stack name was already occupied')
    status, headers, _ = raw_http('POST', '/_simulator/login', urllib.parse.urlencode({'token': TOKEN}).encode(),
                                  {'Origin': ORIGIN, 'Content-Type': 'application/x-www-form-urlencoded'}, timeout=10)
    cookie = headers.get('Set-Cookie', '').split(';', 1)[0]
    check(p, 'console-form-browser-session', status == 303 and
          re.fullmatch(r'tcsim_[0-9a-f]{12}=[0-9a-f]{64}', cookie) is not None)

    # The simulator already owns the configured QEMU runtime; this Node helper
    # executes the production request/response code and no guest commands.
    helper = pathlib.Path(__file__).with_name('ec2_console_form_smoke.mjs').resolve(strict=True)
    node = shutil.which('node')
    if not node:
        raise RuntimeError('Node.js with built-in fetch is required for the optional console form smoke')
    budget = int(min(args.create_timeout, 315) * 1000)
    started = time.time()
    try:
        result = subprocess.run([node, str(helper)], input=json.dumps({
            'endpoint': ENDPOINT, 'cookie': cookie, 'region': REGION, 'stackName': name,
            'imageId': RUNTIME['imageIds'][0], 'python': sys.executable, 'timeout': budget,
        }).encode(), stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env={},
            check=False, timeout=budget / 1000 + 10)
        check(p, 'console-form-wire-succeeded', result.returncode == 0 and len(result.stdout) < 4096,
              exitCode=result.returncode, seconds=round(time.time() - started, 1))
        expected = json.loads(result.stdout)
        stack = exact_stack()
        rows = instance_inventory(ec2)
        outputs = {o['OutputKey']: o['OutputValue'] for o in (stack or {}).get('Outputs', [])}
        check(p, 'console-form-sdk-state-matches', stack is not None and stack['StackStatus'] == 'CREATE_COMPLETE'
              and stack['StackId'] == expected['stackId'] and len(rows) == 1
              and all(outputs.get(k) == expected[v] for k, v in (
                  ('InstanceId', 'id'), ('VpcId', 'vpc'), ('SubnetId', 'subnet')))
              and [outputs.get('GroupId')] == expected['groups']
              and rows[0]['InstanceId'] == expected['id'] and rows[0]['ImageId'] == expected['image']
              and rows[0]['InstanceType'] == expected['type'] and rows[0]['VpcId'] == expected['vpc']
              and rows[0]['SubnetId'] == expected['subnet']
              and [g['GroupId'] for g in rows[0]['SecurityGroups']] == expected['groups'])
        check(p, 'console-form-real-qemu-process', len(qemu_processes()) == 1
              and len(supervisor_processes()) == (1 if sys.platform == 'darwin' else 0))
    finally:
        # A lost mutation response is not a retry cue. Find only the freshly
        # reserved name in this own isolated rehearsal and clean that stack.
        cleanup = exact_stack()
        if cleanup is not None:
            client('cloudformation', read_timeout=120).delete_stack(StackName=cleanup['StackId'])
        deadline = time.monotonic() + 30
        while (qemu_processes() or supervisor_processes()) and time.monotonic() < deadline:
            time.sleep(0.5)
        roles = sorted(role['RoleName'] for role in client('iam').list_roles()['Roles'])
        check(p, 'console-form-stack-process-and-state-cleanup', exact_stack() is None
              and instance_inventory(ec2) == before_instances and roles == before_roles
              and qemu_processes() == [] and supervisor_processes() == [])


def sdk_credentials(c):
    return {'aws_access_key_id': c['AccessKeyId'], 'aws_secret_access_key': c['SecretAccessKey'], 'aws_session_token': c['SessionToken']}


def process_table():
    """(pid, argv, lossy) observations; only Linux preserves argv boundaries."""
    if os.path.isdir('/proc'):
        for pid in os.listdir('/proc'):
            if not pid.isdigit():
                continue
            try:
                raw = pathlib.Path('/proc', pid, 'cmdline').read_bytes().split(b'\0')
            except OSError:
                continue
            if raw and raw[0]:
                if raw[-1] == b'':
                    raw.pop()
                yield int(pid), raw, False
        return
    listing = subprocess.run(['ps', '-axww', '-o', 'pid=,command='], capture_output=True, check=True).stdout
    for line in listing.splitlines():
        pid, _, command = line.strip().partition(b' ')
        if pid.isdigit() and command:
            yield int(pid), command.split(), True


def qemu_executable(value):
    return re.fullmatch(rb'qemu-system-[A-Za-z0-9_-]+', os.path.basename(value)) is not None


def owned_guest_processes(wanted_role):
    """Observe a private QMP path, never infer authority to signal a PID.

    ps cannot recover argument boundaries. Reject ambiguous candidates rather
    than certifying cleanup from a guessed split; never include raw argv in the
    diagnostic. Linux keeps the kernel's NUL-separated argv, including spaces.
    """
    runtime = str(RUNTIME_DIR).encode()
    result = []
    for pid, argv, lossy in process_table():
        role = ('qemu' if qemu_executable(argv[0]) else
                'supervisor' if len(argv) > 3 and argv[1] == b'__tenkacloud-guest-supervisor'
                and argv[2] in (b'none', b'fd3') and qemu_executable(argv[3]) else None)
        if role is not None and role != wanted_role:
            continue
        if role is None and not any(qemu_executable(value) for value in argv):
            continue
        qmp = [argv[i + 1] if i + 1 < len(argv) else b''
               for i, value in enumerate(argv) if value == b'-qmp']
        if not qmp:
            continue
        own = False
        for endpoint in qmp:
            prefix, suffix = b'unix:', b',server=on,wait=off'
            # This rehearsal owns only its private Unix QMP sockets. A TCP
            # monitor, or a Unix socket under another root, is not ours even
            # when that unrelated VM uses different QMP options.
            if endpoint.startswith(b'tcp:'):
                continue
            if not endpoint.startswith(prefix):
                raise RuntimeError('process ownership observation ambiguous')
            if lossy and any(value in runtime for value in (b' ', b'\t', b'\n', b'\r', b'"', b"'", b'\\')):
                raise RuntimeError('process ownership observation ambiguous')
            path = endpoint[len(prefix):]
            if not path.startswith(runtime + b'/'):
                continue
            if not endpoint.endswith(suffix):
                raise RuntimeError('process ownership observation ambiguous')
            path = endpoint[len(prefix):-len(suffix)]
            if (os.path.normpath(path) != path or not path.endswith(b'/qmp.sock')
                    or (lossy and any(value in path for value in (b'"', b"'", b'\\')))):
                raise RuntimeError('process ownership observation ambiguous')
            own = True
        if own:
            if role is None or len(qmp) != 1:
                raise RuntimeError('process ownership observation ambiguous')
            result.append(pid)
    return result


def qemu_processes():
    """QEMU guests of this run."""
    return owned_guest_processes('qemu')


def supervisor_processes():
    """Darwin child supervisors of this run (none exist on Linux)."""
    return owned_guest_processes('supervisor')


def runtime_listing():
    out = []
    for root, dirs, files in os.walk(RUNTIME_DIR):
        for name in sorted(dirs + files):
            full = pathlib.Path(root, name)
            try:
                st = full.lstat()
            except OSError:
                continue
            out.append(f'{oct(st.st_mode)[-4:]} {st.st_size:>11} {full.relative_to(RUNTIME_DIR)}')
    return out


# ---------------------------------------------------------------- probes and scoring
def probe_slot(receipt, slot, wait=12.0):
    mapping = receipt['slots'][slot]
    key = secrets.token_hex(16)
    url = urllib.parse.urlsplit(receipt['binding']['endpoints'][slot]).path
    start = {'version': 1, 'command': 'start', 'binding': mapping['wireBinding'], 'service': mapping['service']['name'], 'key': key,
             'deadline': {'domain': receipt['clockDomain'], 'epochMs': int(time.time() * 1000) + 8000}}
    status, body = operator_json(url + '/start', start)
    if status != 200:
        return {'slot': slot, 'httpStatus': None, 'commandStatus': status, 'error': body.decode(errors='replace')[:200]}
    snapshot = json.loads(body)
    deadline = time.time() + wait
    while not (snapshot and snapshot.get('resultReady') and snapshot.get('settled')) and time.time() < deadline:
        time.sleep(0.25)
        read = {'version': 1, 'command': 'read', 'binding': mapping['wireBinding'], 'service': mapping['service']['name'], 'key': key}
        status, body = operator_json(url + '/read', read)
        if status == 200:
            snapshot = json.loads(body)
    result = (snapshot or {}).get('result') or {}
    observation = result.get('observation') or {}
    return {'slot': slot, 'httpStatus': observation.get('status'), 'error': result.get('error'), 'settled': bool(snapshot and snapshot.get('settled')),
            'resultReady': bool(snapshot and snapshot.get('resultReady')), 'path': observation.get('path'), 'peerPort': (observation.get('route') or {}).get('peerPort'),
            'latencyMs': (observation.get('receivedAtMs') or 0) - (observation.get('startedAtMs') or 0) if observation else None}


def score_round(label, registered, receipt):
    """One scoring tick, following metadata.scoring (uptime-flat) exactly."""
    entry = {'label': label, 'at': stamp(), 'registered': dict(registered), 'observations': []}
    if not all(registered.get(e['slot']) for e in SCORING['endpoints']):
        entry.update(points=0, reason='no registered endpoint (default cfn-output is empty): no probe, no points')
    else:
        ok = True
        for endpoint in SCORING['endpoints']:
            observation = probe_slot(receipt, endpoint['slot'])
            entry['observations'].append(observation)
            ok = ok and observation['httpStatus'] in endpoint['expectStatus'] and observation['path'] == endpoint['path']
        entry['points'] = SCORING['pointsPerSuccess'] if ok else SCORING['failurePenalty']
    report['score']['total'] += entry['points']
    entry['total'] = report['score']['total']
    report['score']['rounds'].append(entry)
    log(f'SCORE {label}')
    save()
    return entry


# ---------------------------------------------------------------- participant terminal
class Terminal:
    """Minimal RFC 6455 client for the Simulator's participant terminal channel."""

    def __init__(self, participant, instance):
        body = json.dumps({'Target': instance, 'Region': REGION})
        request = AWSRequest(method='POST', url=ENDPOINT + '/_simulator/v1/terminal-handoffs', data=body, headers={'Content-Type': 'application/json'})
        SigV4Auth(Credentials(participant['aws_access_key_id'], participant['aws_secret_access_key'], participant['aws_session_token']), 'sts', REGION).add_auth(request)
        status, _, raw = raw_http('POST', '/_simulator/v1/terminal-handoffs', body.encode(), dict(request.headers))
        if status != 200:
            raise RuntimeError(f'terminal handoff refused: {status} {raw[:200]!r}')
        code = json.loads(raw)['Code']
        status, headers, raw = raw_http('POST', '/_simulator/terminal-redeem', urllib.parse.urlencode({'Code': code}).encode(),
                                        {'Origin': ORIGIN, 'Content-Type': 'application/x-www-form-urlencoded'})
        cookie = headers.get('Set-Cookie', '').split(';', 1)[0]
        if status != 303 or '_terminal=' not in cookie:
            raise RuntimeError(f'terminal redeem refused: {status}')
        self.cookie = cookie
        self.instance = instance
        self.sock = None
        self.buffer = b''
        self.output = b''
        self.pings_answered = 0

    def control(self, action, value):
        status, _, raw = raw_http('POST', '/_simulator/v1/terminal-control', json.dumps(value).encode(),
                                  {'Cookie': self.cookie, 'Origin': ORIGIN, 'Content-Type': 'application/x-amz-json-1.1', 'X-Amz-Target': 'AmazonSSM.' + action}, timeout=60)
        return status, raw

    def start(self):
        status, raw = self.control('StartSession', {'Target': self.instance})
        if status != 200:
            raise RuntimeError(f'StartSession refused: {status} {raw[:200]!r}')
        started = json.loads(raw)
        self.session_id = started['SessionId']
        self.sock = socket.create_connection(('127.0.0.1', PORT), timeout=30)
        key = base64.b64encode(os.urandom(16)).decode()
        self.sock.sendall((f'GET /_simulator/v1/terminal-channel/{self.session_id} HTTP/1.1\r\nHost: 127.0.0.1:{PORT}\r\nOrigin: {ORIGIN}\r\n'
                           f'Connection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: {key}\r\nCookie: {self.cookie}\r\n\r\n').encode())
        head = b''
        while b'\r\n\r\n' not in head:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise RuntimeError('terminal upgrade closed')
            head += chunk
        head, self.buffer = head.split(b'\r\n\r\n', 1)
        if b' 101 ' not in head.split(b'\r\n', 1)[0]:
            raise RuntimeError(f'terminal upgrade refused: {head[:120]!r}')
        self.send({'type': 'open', 'token': started['TokenValue']})
        message = self.receive(30)
        if not message or message.get('type') != 'ready':
            raise RuntimeError(f'terminal not ready: {message}')
        return self.session_id

    def send(self, value):
        data = json.dumps(value).encode()
        mask = os.urandom(4)
        header = bytes([0x81])
        header += bytes([0x80 | len(data)]) if len(data) < 126 else bytes([0x80 | 126]) + struct.pack('>H', len(data))
        self.sock.sendall(header + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))

    def _read(self, n, deadline):
        while len(self.buffer) < n:
            remaining = deadline - time.time()
            if remaining <= 0:
                raise TimeoutError
            self.sock.settimeout(remaining)
            try:
                chunk = self.sock.recv(65536)
            except socket.timeout:
                raise TimeoutError
            if not chunk:
                raise ConnectionError('terminal channel closed')
            self.buffer += chunk
        data, self.buffer = self.buffer[:n], self.buffer[n:]
        return data

    def receive(self, timeout):
        deadline = time.time() + timeout
        while True:
            try:
                first = self._read(2, deadline)
            except TimeoutError:
                return None
            op, size = first[0] & 15, first[1] & 127
            if size == 126:
                size = struct.unpack('>H', self._read(2, deadline + 5))[0]
            data = self._read(size, deadline + 5)
            if op == 1:
                return json.loads(data)
            if op == 9:  # answer server liveness pings the way a browser does
                mask = os.urandom(4)
                self.sock.sendall(bytes([0x8A, 0x80 | len(data)]) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))
                self.pings_answered += 1
            if op == 8:
                raise ConnectionError('terminal channel closed by server')

    def type(self, text):
        self.send({'type': 'input', 'data': base64.b64encode(text.encode()).decode()})

    def expect(self, marker, timeout):
        deadline = time.time() + timeout
        while marker.encode() not in self.output:
            remaining = deadline - time.time()
            if remaining <= 0:
                return False
            message = self.receive(min(remaining, 5))
            if message and message.get('type') == 'output':
                self.output += base64.b64decode(message['data'])
        return True

    def run(self, command, timeout=120):
        """Type one shell line and wait for a unique completion marker with its exit code."""
        marker = 'TCDONE' + secrets.token_hex(4)
        begin = len(self.output)
        self.type(f"{command}; echo {marker}:$?:\n")
        deadline = time.time() + timeout
        while True:
            text = self.output[begin:].decode(errors='replace')
            # The echoed command line contains "echo MARKER:$?:"; the result line contains "MARKER:<digits>:".
            match = re.search(marker + r':(\d+):', text)
            if match:
                return int(match.group(1)), text
            remaining = deadline - time.time()
            if remaining <= 0:
                return None, text
            message = self.receive(min(remaining, 5))
            if message and message.get('type') == 'output':
                self.output += base64.b64decode(message['data'])

    def terminate(self):
        status, raw = self.control('TerminateSession', {'SessionId': self.session_id})
        try:
            self.sock.close()
        except OSError:
            pass
        return status


def send_command(ssm, instance, commands, label, entry, timeout=180):
    sent = ssm.send_command(DocumentName='AWS-RunShellScript', InstanceIds=[instance], Parameters={'commands': commands})
    command_id = sent['Command']['CommandId']
    deadline = time.time() + timeout
    invocation = {}
    while time.time() < deadline:
        time.sleep(1.0)
        try:
            invocation = ssm.get_command_invocation(CommandId=command_id, InstanceId=instance)
        except ClientError as error:
            invocation = {'Status': 'Error:' + error.response['Error']['Code']}
        if invocation.get('Status') in ('Success', 'Failed', 'TimedOut', 'Cancelled'):
            break
    check(entry, label, invocation.get('Status') == 'Success' and invocation.get('ResponseCode') == 0,
          commands=commands, status=invocation.get('Status'), responseCode=invocation.get('ResponseCode'),
          stdout=(invocation.get('StandardOutputContent') or '')[:300], stderr=(invocation.get('StandardErrorContent') or '')[:300])
    return invocation


@contextmanager
def disruption_role(iam, trust, policy):
    """Scope the driver's own IAM role to grading; it is not part of the stack."""
    name = 'RehearsalDisruptionOperator-' + secrets.token_hex(8)
    created = iam.create_role(RoleName=name, AssumeRolePolicyDocument=json.dumps(trust))['Role']
    role_id, arn = created['RoleId'], created['Arn']
    if not isinstance(role_id, str) or not role_id or not isinstance(arn, str) or not arn:
        raise RuntimeError('disruption role creation did not confirm its identity')

    def missing(error):
        return (error.response.get('Error', {}).get('Code') == 'NoSuchEntity'
                and error.response.get('ResponseMetadata', {}).get('HTTPStatusCode') == 404)

    try:
        iam.put_role_policy(RoleName=name, PolicyName='Disruption', PolicyDocument=json.dumps(policy))
        yield arn
    finally:
        # Only our positively identified role in this driver's private simulator.
        # Do not delete another role if our name or ownership is ambiguous.
        if iam.get_role(RoleName=name)['Role']['RoleId'] != role_id:
            raise RuntimeError('disruption role identity changed')
        try:
            iam.delete_role_policy(RoleName=name, PolicyName='Disruption')
        except ClientError as error:
            if not missing(error):  # a failed Put may not have installed the policy
                raise
        iam.delete_role(RoleName=name)
        try:
            iam.get_role(RoleName=name)
        except ClientError as error:
            if not missing(error):
                raise
        else:
            raise RuntimeError('disruption role still present after deletion')


def stop_here(name):
    return args.stop_after == name


exit_code = 1
stack_id = ''
try:
    # ------------------------------------------------------------ startup
    p = phase('startup')
    deadline = time.time() + 600  # bundle extraction verifies every byte of the pinned archive first
    while True:
        if process.poll() is not None:
            sim_log.flush()
            tail = (WORK / 'simulator.log').read_text(errors='replace').strip().splitlines()[-3:]
            raise RuntimeError(f'simulator exited during startup: {process.returncode}: {" | ".join(tail)[-400:]}')
        if token_path.exists():
            TOKEN = token_path.read_text().strip()
            try:
                status, _, _ = raw_http('GET', '/_simulator/health', headers={'X-Simulator-Token': TOKEN}, timeout=2)
                if status == 200:
                    break
            except OSError:
                pass
        if time.time() > deadline:
            raise RuntimeError('simulator readiness timed out')
        time.sleep(0.2)
    check(p, 'simulator-ready-with-pinned-bundle', True, startupSeconds=round(time.time() - T0, 1))
    status, body = operator_json('/_simulator/v1/battle-capabilities/availability', {'version': 1})
    availability = json.loads(body) if status == 200 else {}
    check(p, 'battle-runtime-configured', status == 200 and availability.get('configured') is True, availability=availability)
    caller = client('sts').get_caller_identity()
    check(p, 'native-operator-identity', caller['Account'] == '999999999999', account=caller['Account'])
    check(p, 'no-vm-before-deployment', qemu_processes() == [] and supervisor_processes() == [], qemuPids=qemu_processes(), supervisorPids=supervisor_processes())
    check(p, 'ec2-readback-requires-local-token', missing_instance_token_is_denied())
    ec2 = instance_read_client(TOKEN)
    before_instances = instance_inventory(ec2)
    check(p, 'ec2-inventory-empty-before-stack', before_instances == [])
    if args.console_form_smoke:
        console_form_smoke(ec2, before_instances)
        raise SystemExit(0)  # Separate witness: execute normal shutdown, not the Challenge.

    # ------------------------------------------------------------ initialize
    p = phase('initialize')
    prefix = 'tc-hello-world-battle-team1'
    external_id = 'rehearsal-' + secrets.token_hex(12)
    cfn = client('cloudformation', read_timeout=args.create_timeout)
    parameters = {'NamePrefix': prefix, 'TenkaCloudAccountId': '999999999999', 'ExternalId': external_id}
    started = time.time()
    diagnostics = p['stackCreateDiagnostics'] = {}
    try:
        created = cfn.create_stack(StackName=prefix, TemplateBody=TEMPLATE.decode(), Capabilities=['CAPABILITY_NAMED_IAM'],
                                   Parameters=[{'ParameterKey': k, 'ParameterValue': v} for k, v in parameters.items()])
        stack_id = created['StackId']
    except Exception as error:  # read back committed state even without a successful SDK result
        diagnostics['createStack'] = 'malformed-service-error'
        diagnostics['createStack'] = stack_request_error(error, 'Code', 'Message')
    else:
        diagnostics['createStack'] = 'response-confirmed'
    finally:
        diagnostics['simulatorAtCreateReply'] = simulator_process_state()
    check(p, 'create-stack-response', True, seconds=round(time.time() - started, 1),
          responded=diagnostics['createStack'] == 'response-confirmed')
    submitted_stack_id = stack_id
    describe = client('cloudformation')
    stack = None
    deadline = time.time() + args.create_timeout
    while time.time() < deadline:
        try:
            stacks = describe.describe_stacks(StackName=stack_id or prefix)['Stacks']
        except ClientError as error:
            stacks = []
            diagnostics['describeStacks'] = 'malformed-service-error'
            try:
                diagnostics['describeStacks'] = stack_request_error(error, 'Code')
            except (AttributeError, KeyError, TypeError):
                diagnostics['simulatorAtReadback'] = simulator_process_state()
                raise
        except Exception as error:
            diagnostics['describeStacks'] = stack_request_error(error)
            diagnostics['simulatorAtReadback'] = simulator_process_state()
            raise
        else:
            diagnostics['describeStacks'] = 'stack-observed' if stacks else 'no-stack-observed'
        if stacks:
            stack = stacks[0]
            stack_id = stack['StackId']
            if stack['StackStatus'] != 'CREATE_IN_PROGRESS':
                break
        time.sleep(2)
    event_page = {}
    try:
        event_page = describe.describe_stack_events(StackName=stack_id) if stack_id else {}
    except ClientError as error:
        diagnostics['describeStackEvents'] = stack_request_error(error)
    except Exception as error:
        diagnostics['describeStackEvents'] = stack_request_error(error)
        raise
    else:
        diagnostics['describeStackEvents'] = 'malformed-event-page'
        diagnostics['describeStackEvents'] = stack_event_observation(event_page, stack_id)
    finally:
        diagnostics['simulatorAtReadback'] = simulator_process_state()
    events = event_page.get('StackEvents', [])
    check(p, 'stack-create-complete', bool(stack) and stack['StackStatus'] == 'CREATE_COMPLETE', status=stack and stack['StackStatus'],
          seconds=round(time.time() - started, 1))
    outputs = {o['OutputKey']: o['OutputValue'] for o in stack.get('Outputs', [])}
    instance = outputs['InstanceId']
    host_hint = outputs['Ec2HostHint']
    check(p, 'outputs-default-endpoints-empty', outputs.get('FrontendUrl') == '' and outputs.get('ApiUrl') == '' and bool(host_hint) and instance.startswith('i-'),
          frontendUrl=outputs.get('FrontendUrl'), apiUrl=outputs.get('ApiUrl'), ec2HostHint=host_hint, instanceId=instance)
    pids = qemu_processes()
    check(p, 'one-real-qemu-guest-process', len(pids) == 1 and len(supervisor_processes()) == (1 if sys.platform == 'darwin' else 0),
          qemuPids=pids, supervisorPids=supervisor_processes())
    report['runtimeDirectoryAfterLaunch'] = runtime_listing()
    owner = {'eventId': 'rehearsal-event-1', 'teamId': 'team1', 'jobId': 'job-' + secrets.token_hex(6), 'unitHash': hashlib.sha256(TEMPLATE).hexdigest(),
             'deployedAt': int(time.time() * 1000), 'stackId': stack_id, 'instanceId': instance}
    digest = hashlib.sha256()
    for value in [owner['eventId'], owner['teamId'], owner['jobId'], owner['unitHash'], str(owner['deployedAt']), owner['stackId'], owner['instanceId']]:
        digest.update((str(len(value.encode())) + ':').encode() + value.encode())
    owner_key = digest.hexdigest()
    status, body = operator_json('/_simulator/v1/battle-capabilities/resolve', {'version': 1, 'owner': owner, 'key': owner_key})
    receipt = json.loads(body) if status == 200 else {}
    check(p, 'battle-capability-receipt', status == 200 and set(receipt.get('slots', {})) == {'frontend', 'api'}, httpStatus=status,
          clockDomain=receipt.get('clockDomain'), slots={k: v['service'] for k, v in receipt.get('slots', {}).items()},
          target={k: v['binding']['target'] for k, v in receipt.get('slots', {}).items()})
    check(p, 'ec2-readback-matches-completed-stack-instance',
          stack.get('StackName') == prefix and (not submitted_stack_id or submitted_stack_id == stack_id)
          and not event_page.get('NextToken')
          and matches_completed_stack_instance(events, stack_id, instance, instance_inventory(ec2)))
    if stop_here('initialize'):
        raise SystemExit(0)

    # ------------------------------------------------------------ operate: participant connects and registers
    p = phase('operate')
    registered = {}
    score_round('before-registration', registered, receipt)
    check(p, 'no-points-before-registration', report['score']['total'] == 0, total=report['score']['total'])
    viewer = client('sts').assume_role(RoleArn=outputs['ParticipantViewerRoleArn'], RoleSessionName='participant-team1', ExternalId=external_id, DurationSeconds=3600)['Credentials']
    participant = sdk_credentials(viewer)
    identity = client('sts', participant).get_caller_identity()
    check(p, 'participant-assumed-original-viewer-role', identity['Account'] == '000000000000', arn=identity['Arn'])
    # A session started before the guest's relay answers fails and, by design, keeps the target
    # busy. Open it only after the guest serves the original UserData's api (owned probe),
    # as a participant who first registers endpoints and waits for scoring does.
    waited = time.time()
    api = None
    while time.time() - waited < args.userdata_timeout:
        api = probe_slot(receipt, 'api')
        if api.get('httpStatus') == 200:
            break
        time.sleep(10)
    check(p, 'guest-api-answers-before-terminal', bool(api) and api.get('httpStatus') == 200, seconds=round(time.time() - waited, 1), observation=api)
    terminal = Terminal(participant, instance)
    session_id = terminal.start()
    check(p, 'participant-session-manager-shell-ready', True, sessionIdPrefix=session_id[:12])
    terminal.expect('$', 30)
    code, text = terminal.run('id -un; hostname; cat /etc/system-release', 60)
    check(p, 'participant-shell-executes-in-guest', code == 0 and 'Amazon Linux' in text, exitCode=code, transcript=text[-400:])
    # Original UserData is asynchronous cloud-init work, as on EC2. Wait for it from inside the guest.
    waited = time.time()
    code, text = terminal.run('sudo cloud-init status --wait; sudo cloud-init status --long | head -12', args.userdata_timeout)
    check(p, 'original-userdata-finished', code is not None, exitCode=code, seconds=round(time.time() - waited, 1), transcript=text[-900:])
    code, text = terminal.run("rpm -q nginx python3; systemctl is-active nginx tenkacloud-api; curl -s -o /dev/null -w 'frontend=%{http_code}\\n' http://127.0.0.1/; curl -s -w ' api=%{http_code}\\n' http://127.0.0.1:8080/healthz", 120)
    check(p, 'userdata-installed-and-started-services', code == 0 and 'frontend=200' in text and 'api=200' in text, exitCode=code, transcript=text[-700:])
    code, text = terminal.run('cat /etc/profile.d/tenkacloud-hello.sh | grep -E "FrontendUrl|ApiUrl"', 60)
    check(p, 'imds-public-hostname-in-login-banner', code == 0 and host_hint in text, exitCode=code, transcript=text[-400:])
    # A participant who only watches output types nothing. The channel must stay attached well
    # past a minute; this driver answers WebSocket pings the way a browser does and sends no text.
    watched = time.time()
    code, text = terminal.run('for i in $(seq 1 150); do echo watching-line-$i; sleep 0.5; done', 300)
    lines = len(set(re.findall(r'watching-line-(\d+)\r', text)))
    check(p, 'watching-participant-stays-attached-past-a-minute', code == 0 and lines == 150 and time.time() - watched > 65,
          exitCode=code, linesSeen=lines, seconds=round(time.time() - watched, 1), pingsAnswered=terminal.pings_answered)
    # Participant registers the two endpoints exactly as the instructions say.
    for slot, url in (('frontend', f'http://{host_hint}'), ('api', f'http://{host_hint}:8080')):
        parsed = urllib.parse.urlsplit(url)
        target = receipt['slots'][slot]['binding']['target']
        port = parsed.port or 80
        if parsed.hostname != host_hint or port != target['peerPort']:
            raise RuntimeError('registered endpoint does not match the deployment receipt')
        registered[slot] = url
    check(p, 'participant-registered-both-endpoints', True, registered=registered)
    terminal.terminate()
    if stop_here('operate'):
        raise SystemExit(0)

    # ------------------------------------------------------------ grade + disruption + recovery
    p = phase('grade')
    first = score_round('healthy-1', registered, receipt)
    check(p, 'healthy-round-awards-points', first['points'] == SCORING['pointsPerSuccess'], points=first['points'], observations=first['observations'])
    time.sleep(args.probe_interval)
    second = score_round('healthy-2', registered, receipt)
    check(p, 'second-minute-awards-points', second['points'] == SCORING['pointsPerSuccess'], points=second['points'])

    # Operator disruption exactly as metadata.disruptions[0].action describes.
    disruption = METADATA['disruptions'][0]['action']
    iam = client('iam')
    ops_external = 'ops-' + secrets.token_hex(12)
    trust = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Principal': {'AWS': 'arn:aws:iam::999999999999:root'}, 'Action': 'sts:AssumeRole',
                                                     'Condition': {'StringEquals': {'sts:ExternalId': ops_external}}}]}
    policy = {'Version': '2012-10-17', 'Statement': [
        {'Effect': 'Allow', 'Action': 'ssm:SendCommand', 'Resource': [f'arn:aws:ssm:{REGION}::document/{disruption["documentName"]}', f'arn:aws:ec2:{REGION}:000000000000:instance/{instance}']},
        {'Effect': 'Allow', 'Action': ['ssm:GetCommandInvocation', 'ssm:ListCommands'], 'Resource': '*'}]}
    with disruption_role(iam, trust, policy) as role_arn:
        ops = sdk_credentials(client('sts').assume_role(RoleArn=role_arn, RoleSessionName='disruption', ExternalId=ops_external, DurationSeconds=3600)['Credentials'])
        ssm = client('ssm', ops)
        send_command(ssm, instance, disruption['paramTemplate']['commands'], 'operator-disruption-run-shell-script', p)
        time.sleep(2)
        down = score_round('after-disruption', registered, receipt)
        frontend = [o for o in down['observations'] if o['slot'] == 'frontend'][0]
        api = [o for o in down['observations'] if o['slot'] == 'api'][0]
        check(p, 'disruption-stops-frontend-and-penalizes', down['points'] == SCORING['failurePenalty'] and frontend['httpStatus'] != 200 and api['httpStatus'] == 200,
              points=down['points'], frontend=frontend, api=api)

        # Participant recovers early through Session Manager.
        terminal = Terminal(participant, instance)
        terminal.start()
        terminal.expect('$', 30)
        code, text = terminal.run('systemctl is-active nginx; sudo systemctl start nginx && systemctl is-active nginx', 120)
        check(p, 'participant-recovers-with-systemctl-start-nginx', code == 0 and 'inactive' in text and '\nactive' in text.replace('\r', ''), exitCode=code, transcript=text[-400:])
        terminal.terminate()
        recovered = score_round('after-participant-recovery', registered, receipt)
        check(p, 'recovery-restores-points', recovered['points'] == SCORING['pointsPerSuccess'], points=recovered['points'])

        # Second disruption, then the catalog's revert command (the 600 s timer itself is a host duty and is not waited here).
        send_command(ssm, instance, disruption['paramTemplate']['commands'], 'operator-second-disruption', p)
        time.sleep(2)
        down2 = score_round('after-second-disruption', registered, receipt)
        check(p, 'second-disruption-penalizes', down2['points'] == SCORING['failurePenalty'], points=down2['points'])
        send_command(ssm, instance, disruption['revert']['paramTemplate']['commands'], 'operator-revert-run-shell-script', p)
        time.sleep(2)
        reverted = score_round('after-operator-revert', registered, receipt)
        check(p, 'revert-restores-points', reverted['points'] == SCORING['pointsPerSuccess'], points=reverted['points'])
    check(p, 'disruption-operator-role-removed', True)
    expected = sum(r['points'] for r in report['score']['rounds'])
    check(p, 'score-ledger-consistent', report['score']['total'] == expected, total=report['score']['total'],
          ledger=[(r['label'], r['points']) for r in report['score']['rounds']])
    if stop_here('grade'):
        raise SystemExit(0)

    # ------------------------------------------------------------ cleanup
    p = phase('cleanup')
    before = qemu_processes()
    status, body = operator_json('/_simulator/v1/battle-capabilities/seal', {'version': 1, 'owner': owner, 'key': owner_key}, timeout=30)
    sealed = json.loads(body) if status == 200 else {}
    check(p, 'capability-sealed-and-joined', status == 200 and sealed.get('sealed') is True and sealed.get('joined') is True, httpStatus=status, result=sealed)
    late = probe_slot(receipt, 'frontend', wait=3)
    check(p, 'probe-refused-after-seal', late.get('httpStatus') is None, observation=late)
    started = time.time()
    delete_error = None
    try:
        client('cloudformation', read_timeout=120).delete_stack(StackName=stack_id)
    except ClientError as error:
        delete_error = f"{error.response['Error']['Code']}: {error.response['Error']['Message']}"
    except Exception as error:
        delete_error = f'{type(error).__name__}: {str(error)[:300]}'
    final = None
    deadline = time.time() + 120
    while time.time() < deadline:
        try:
            found = describe.describe_stacks(StackName=stack_id)['Stacks']
            final = found[0]['StackStatus'] if found else 'UNKNOWN'
        except ClientError as error:
            # Only the simulator's explicit named-stack absence proves deletion.
            # Other API errors leave cleanup unconfirmed and must propagate.
            detail = error.response.get('Error', {})
            status = error.response.get('ResponseMetadata', {}).get('HTTPStatusCode')
            if (detail.get('Code') != 'ValidationError'
                    or detail.get('Message') != 'Stack does not exist.' or status != 400):
                raise
            final = 'GONE'
        if final in ('GONE', 'DELETE_COMPLETE', 'DELETE_FAILED'):
            break
        time.sleep(1)
    check(p, 'stack-deleted', delete_error is None and final in ('GONE', 'DELETE_COMPLETE'), deleteError=delete_error, finalStatus=final, seconds=round(time.time() - started, 1))
    deadline = time.time() + 30
    while (qemu_processes() or supervisor_processes()) and time.time() < deadline:
        time.sleep(0.5)
    check(p, 'guest-process-exited', qemu_processes() == [] and supervisor_processes() == [], before=before, after=qemu_processes(),
          supervisorsAfter=supervisor_processes())
    roles = [r['RoleName'] for r in client('iam').list_roles()['Roles']]
    check(p, 'stack-iam-roles-removed', not any(r.startswith(prefix) for r in roles), remainingRoles=roles)
    check(p, 'ec2-inventory-restored-after-stack-delete', instance_inventory(ec2) == before_instances)
    exit_code = 0
except SystemExit as stop:
    exit_code = stop.code if type(stop.code) is int and -255 <= stop.code <= 255 else (0 if stop.code is None else 1)
except BaseException as error:  # the failure is preserved in the report, never converted to success
    report['failure'] = public_failure(error)
    log('FAILURE')
    sim_log.flush()
finally:
    p = phase('shutdown')
    report['runtimeDirectoryBeforeShutdown'] = runtime_listing()
    report['qemuBeforeShutdown'] = qemu_processes()
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
    try:
        process.wait(timeout=60)
    except subprocess.TimeoutExpired:
        report['shutdownTimedOut'] = True
        process.kill()
        process.wait()
    sim_log.close()
    time.sleep(1)
    leftovers = qemu_processes() + supervisor_processes()
    listing = runtime_listing()
    report['runtimeDirectoryAfterShutdown'] = listing
    sockets = [line for line in listing if line.endswith('.sock')]
    shutdown_ok = process.returncode == 0 and leftovers == []
    p['checks'].append({'name': 'simulator-exit-and-no-guest-left', 'passed': shutdown_ok, 'exitCode': process.returncode, 'qemuPids': leftovers,
                        'remainingRuntimeEntries': len(listing), 'remainingSockets': sockets})
    log('SHUTDOWN')
    if not shutdown_ok:
        exit_code = 1
    report['result'] = 'PASS' if exit_code == 0 and 'failure' not in report and not args.stop_after else ('PARTIAL' if exit_code == 0 else 'FAIL')
    report['finishedAt'] = stamp()
    save()
    log(f"RESULT {report['result']}")
sys.exit(exit_code)
