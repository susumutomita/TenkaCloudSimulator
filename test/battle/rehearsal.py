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
import hashlib
import json
import os
import pathlib
import re
import secrets
import signal
import socket
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

os.environ.update(AWS_CONFIG_FILE='/dev/null', AWS_SHARED_CREDENTIALS_FILE='/dev/null', AWS_EC2_METADATA_DISABLED='true')
for name in ('HTTP_PROXY', 'HTTPS_PROXY', 'http_proxy', 'https_proxy', 'ALL_PROXY', 'all_proxy'):
    os.environ.pop(name, None)
import boto3  # noqa: E402
from botocore.auth import SigV4Auth  # noqa: E402
from botocore.awsrequest import AWSRequest  # noqa: E402
from botocore.config import Config  # noqa: E402
from botocore.credentials import Credentials  # noqa: E402
from botocore.exceptions import ClientError  # noqa: E402

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


def stamp():
    return time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())


def log(message):
    print(f'[{stamp()} +{time.time() - T0:7.1f}s] {message}', flush=True)


def save():
    REPORT.write_text(json.dumps(report, indent=1, sort_keys=True) + '\n')


def phase(name):
    entry = {'name': name, 'startedAt': stamp(), 'checks': []}
    report['phases'].append(entry)
    log(f'=== {name} ===')
    return entry


def check(entry, name, passed, **detail):
    item = {'name': name, 'passed': bool(passed), 'at': stamp(), **detail}
    entry['checks'].append(item)
    log(f"{'PASS' if passed else 'FAIL'} {name} {json.dumps(detail, sort_keys=True, default=str)[:600]}")
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
sim_log = open(WORK / 'simulator.log', 'wb')
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


def sdk_credentials(c):
    return {'aws_access_key_id': c['AccessKeyId'], 'aws_secret_access_key': c['SecretAccessKey'], 'aws_session_token': c['SessionToken']}


def process_table():
    """(pid, argv0, full command line) for every process; /proc on Linux, ps on macOS."""
    if os.path.isdir('/proc'):
        for pid in os.listdir('/proc'):
            if not pid.isdigit():
                continue
            try:
                raw = pathlib.Path('/proc', pid, 'cmdline').read_bytes().split(b'\0')
            except OSError:
                continue
            if raw and raw[0]:
                yield int(pid), raw[0], b' '.join(raw)
        return
    listing = subprocess.run(['ps', '-axww', '-o', 'pid=,command='], capture_output=True, check=True).stdout
    for line in listing.splitlines():
        pid, _, command = line.strip().partition(b' ')
        if pid.isdigit() and command:
            yield int(pid), command.split(b' ', 1)[0], command


def qemu_processes():
    """QEMU guests of this run."""
    runtime = str(RUNTIME_DIR).encode()
    return [pid for pid, argv0, command in process_table() if runtime in command and b'qemu-system' in argv0]


def supervisor_processes():
    """Darwin child supervisors of this run (none exist on Linux)."""
    runtime = str(RUNTIME_DIR).encode()
    return [pid for pid, argv0, command in process_table()
            if runtime in command and b'__tenkacloud-guest-supervisor' in command and b'qemu-system' not in argv0]


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
    log(f"SCORE {label}: {entry['points']:+d} (total {entry['total']}) {json.dumps(entry['observations'], sort_keys=True)[:400]}")
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

    # ------------------------------------------------------------ initialize
    p = phase('initialize')
    prefix = 'tc-hello-world-battle-team1'
    external_id = 'rehearsal-' + secrets.token_hex(12)
    cfn = client('cloudformation', read_timeout=args.create_timeout)
    parameters = {'NamePrefix': prefix, 'TenkaCloudAccountId': '999999999999', 'ExternalId': external_id}
    started = time.time()
    create_error = None
    try:
        created = cfn.create_stack(StackName=prefix, TemplateBody=TEMPLATE.decode(), Capabilities=['CAPABILITY_NAMED_IAM'],
                                   Parameters=[{'ParameterKey': k, 'ParameterValue': v} for k, v in parameters.items()])
        stack_id = created['StackId']
    except ClientError as error:
        create_error = f"{error.response['Error']['Code']}: {error.response['Error']['Message']}"
    except Exception as error:  # transport loss is recorded; the outcome is then observed, never assumed
        create_error = f'{type(error).__name__}: {str(error)[:300]}'
    check(p, 'create-stack-response', True, seconds=round(time.time() - started, 1), error=create_error, responded=create_error is None)
    describe = client('cloudformation')
    stack = None
    deadline = time.time() + args.create_timeout
    while time.time() < deadline:
        try:
            stacks = describe.describe_stacks(StackName=stack_id or prefix)['Stacks']
        except ClientError as error:
            stacks = []
            last = error.response['Error']['Code']
        if stacks:
            stack = stacks[0]
            stack_id = stack['StackId']
            if stack['StackStatus'] != 'CREATE_IN_PROGRESS':
                break
        time.sleep(2)
    events = []
    try:
        events = describe.describe_stack_events(StackName=stack_id)['StackEvents'] if stack_id else []
    except ClientError:
        pass
    report['stackEvents'] = [{'logicalId': e.get('LogicalResourceId'), 'type': e.get('ResourceType'), 'status': e.get('ResourceStatus'), 'reason': e.get('ResourceStatusReason')} for e in events]
    check(p, 'stack-create-complete', bool(stack) and stack['StackStatus'] == 'CREATE_COMPLETE', status=stack and stack['StackStatus'],
          reason=stack and stack.get('StackStatusReason'), seconds=round(time.time() - started, 1), createError=create_error)
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
    code, text = terminal.run("sudo tail -n 25 /var/log/cloud-init-output.log", 60)
    report['cloudInitOutputTail'] = text[-3000:]
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
    role = 'RehearsalDisruptionOperator'
    ops_external = 'ops-' + secrets.token_hex(12)
    trust = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Principal': {'AWS': 'arn:aws:iam::999999999999:root'}, 'Action': 'sts:AssumeRole',
                                                     'Condition': {'StringEquals': {'sts:ExternalId': ops_external}}}]}
    iam.create_role(RoleName=role, AssumeRolePolicyDocument=json.dumps(trust))
    policy = {'Version': '2012-10-17', 'Statement': [
        {'Effect': 'Allow', 'Action': 'ssm:SendCommand', 'Resource': [f'arn:aws:ssm:{REGION}::document/{disruption["documentName"]}', f'arn:aws:ec2:{REGION}:000000000000:instance/{instance}']},
        {'Effect': 'Allow', 'Action': ['ssm:GetCommandInvocation', 'ssm:ListCommands'], 'Resource': '*'}]}
    iam.put_role_policy(RoleName=role, PolicyName='Disruption', PolicyDocument=json.dumps(policy))
    ops = sdk_credentials(client('sts').assume_role(RoleArn=f'arn:aws:iam::000000000000:role/{role}', RoleSessionName='disruption', ExternalId=ops_external, DurationSeconds=3600)['Credentials'])
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
            final = found[0]['StackStatus'] if found else 'GONE'
        except ClientError as error:
            final = 'GONE:' + error.response['Error']['Code']
        if final.startswith('GONE') or final in ('DELETE_COMPLETE', 'DELETE_FAILED'):
            break
        time.sleep(1)
    check(p, 'stack-deleted', delete_error is None and (final.startswith('GONE') or final == 'DELETE_COMPLETE'), deleteError=delete_error, finalStatus=final, seconds=round(time.time() - started, 1))
    deadline = time.time() + 30
    while (qemu_processes() or supervisor_processes()) and time.time() < deadline:
        time.sleep(0.5)
    check(p, 'guest-process-exited', qemu_processes() == [] and supervisor_processes() == [], before=before, after=qemu_processes(),
          supervisorsAfter=supervisor_processes())
    roles = [r['RoleName'] for r in client('iam').list_roles()['Roles']]
    check(p, 'stack-iam-roles-removed', not any(r.startswith(prefix) for r in roles), remainingRoles=roles)
    exit_code = 0
except SystemExit as stop:
    exit_code = int(stop.code or 0)
except BaseException as error:  # the failure is preserved in the report, never converted to success
    report['failure'] = f'{type(error).__name__}: {str(error)[:500]}'
    log('FAILURE ' + report['failure'])
    sim_log.flush()
    # The operator log names internal causes the API deliberately withholds.
    for line in (WORK / 'simulator.log').read_text(errors='replace').splitlines()[-15:]:
        log('SIMULATOR ' + line[:400])
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
    log(f"{'PASS' if shutdown_ok else 'FAIL'} simulator exit {process.returncode}, qemu left {leftovers}, runtime entries {len(listing)}")
    if not shutdown_ok:
        exit_code = 1
    report['result'] = 'PASS' if exit_code == 0 and 'failure' not in report and not args.stop_after else ('PARTIAL' if exit_code == 0 else 'FAIL')
    report['finishedAt'] = stamp()
    save()
    log(f"RESULT {report['result']} score={report['score']['total']}")
sys.exit(exit_code)
