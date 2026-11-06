"""Original crypto template through two real CLIs and the development boto3 SDK.

This checks provisioning/permissions/cleanup, not game coordination or scoring.
All values are generated local dummy inputs; host-issued participant credentials
are not STS. No template edit, real cloud call, or secret value report is allowed.
"""
import contextlib
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import urllib.error
import urllib.request
from urllib.parse import urlencode, urlsplit

ROOT = Path(__file__).resolve().parents[2]
os.environ.setdefault('SIMULATOR_BINARY', str(ROOT / 'dist/tenkacloud-simulator'))
os.environ.setdefault('SIMULATOR_REPORTS', str(ROOT / 'reports'))
from botocore.exceptions import ClientError
from check import running, boto3, Config, REPORTS, BINARY

FIXTURE = ROOT / 'test/fixtures/crypto-battle-a749/template.yaml'
FIXTURE_SHA = 'fb2af9be3ba67619b80de0ad8f00dc102aaade740742aea5861c9039d8bd09e6'
REGION = 'ap-northeast-1'
PREFIX = 'tc-crypto-team'
ITEM = '/' + PREFIX + '/score-item'
PRIVATE = '/crypto-private/local-dummy'
STACK = 'crypto-original'


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise AssertionError('Owned local request attempted redirect')


def frame(database):
    """Read one SQLite snapshot; report only its digest, never stored values."""
    connection = sqlite3.connect(database.as_uri() + '?mode=ro', uri=True)
    try:
        connection.execute('BEGIN')
        names = [r[0] for r in connection.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
        result = []
        for name in names:
            quoted = '"' + name.replace('"', '""') + '"'
            rows = []
            for row in connection.execute('SELECT * FROM ' + quoted):
                rows.append(json.dumps([{'blob': v.hex()} if isinstance(v, bytes) else v
                                        for v in row], sort_keys=True))
            result.append((name, sorted(rows)))
        return hashlib.sha256(json.dumps(result, sort_keys=True).encode()).hexdigest()
    finally:
        connection.close()


class Team:
    def __init__(self, endpoint, token, database):
        self.endpoint, self.token, self.database = endpoint, token, database
        self.origin = urlsplit(endpoint)
        assert self.origin.scheme == 'http' and self.origin.hostname == '127.0.0.1' and self.origin.port
        self.counts = {'sdkCall': 0, 'sdkSend': 0, 'sdkResponse': 0, 'sdkParsed': 0,
                       'nativeSend': 0, 'nativeResponse': 0}
        self.clients = []
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        try:
            self.cf = self.client('cloudformation')
            self.ssm = self.client('ssm')
            self.iam = self.client('iam')
        except BaseException:
            self.close()
            raise

    def guard(self, url):
        parsed = urlsplit(url)
        assert parsed.scheme == self.origin.scheme and parsed.netloc == self.origin.netloc
        assert parsed.hostname == '127.0.0.1' and not parsed.username and not parsed.password

    def client(self, service, token=None):
        token = self.token if token is None else token
        client = boto3.client(service, endpoint_url=self.endpoint, region_name=REGION,
            aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy',
            config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=5, read_timeout=10))
        def sign(request, **kwargs):
            self.guard(request.url)
            request.headers.add_header('X-Simulator-Token', token)
        def send(request, **kwargs):
            self.guard(request.url)
            self.counts['sdkSend'] += 1
        def call(params, **kwargs):
            self.guard(params['url'])
            self.counts['sdkCall'] += 1
        def response(response_dict, **kwargs):
            # Real HTTP status is observed BEFORE botocore's body parser. A
            # parse exception cannot make the response counter vanish.
            assert type(response_dict['status_code']) is int
            assert 100 <= response_dict['status_code'] <= 599
            self.counts['sdkResponse'] += 1
        def parsed(**kwargs):
            self.counts['sdkParsed'] += 1
        client.meta.events.register('before-call.' + service, call)
        client.meta.events.register('before-sign.' + service, sign)
        client.meta.events.register('before-send.' + service, send)
        client.meta.events.register('before-parse.' + service, response)
        client.meta.events.register('after-call.' + service, parsed)
        self.clients.append(client)
        return client

    def wire(self, path, token=None, value=None, status=200, form=None):
        target = self.endpoint + path
        self.guard(target)
        assert path.startswith('/') and not path.startswith('//')
        headers = {'X-Simulator-Token': self.token if token is None else token}
        body = None
        if value is not None:
            body = json.dumps(value).encode()
            headers['Content-Type'] = 'application/json'
        if form is not None:
            assert value is None
            body = urlencode(form).encode()
            headers.update({'Content-Type': 'application/x-www-form-urlencoded',
                            'X-Simulator-Service': 'cloudformation'})
        self.counts['nativeSend'] += 1
        request = urllib.request.Request(target, data=body, headers=headers,
                                         method='POST' if body is not None else 'GET')
        try:
            result = self.opener.open(request, timeout=5)
        except urllib.error.HTTPError as error:
            result = error
        with result:
            data = result.read()
            self.counts['nativeResponse'] += 1
            assert result.code == status, 'Unexpected owned native response status'
            return data

    def participant(self, role):
        body = self.wire('/_simulator/v1/access', value={'Principal': role})
        value = json.loads(body)['AccessToken']
        assert isinstance(value, str) and len(value) >= 32
        return value

    def close(self):
        for client in self.clients:
            client.close()


def refused(operation, code, status):
    try:
        operation()
    except ClientError as error:
        assert error.response['Error']['Code'] == code
        assert error.response['ResponseMetadata']['HTTPStatusCode'] == status
    else:
        raise AssertionError('Expected native SDK refusal')


def parameters(seed, enabled):
    supplied = {'NamePrefix': PREFIX, 'TenkaCloudAccountId': '000000000000',
                'ExternalId': 'LOCALDUMMYEXTERNAL', 'ScoreStealSeed': seed}
    if enabled:
        supplied['ScoreStealEnabled'] = 'true'
    # ScoreStealKey=3 and ScoreStealEnabled=false are real template defaults.
    return [{'ParameterKey': k, 'ParameterValue': v} for k, v in sorted(supplied.items())]


def described(team, stack=STACK):
    value = team.cf.describe_stacks(StackName=stack)['Stacks']
    assert len(value) == 1 and value[0]['StackStatus'] == 'CREATE_COMPLETE'
    return value[0], {o['OutputKey']: o['OutputValue'] for o in value[0]['Outputs']}


def inspect(team, seed, enabled):
    record, outputs = described(team)
    assert outputs['CoordinationItemEnabled'] == ('true' if enabled else 'false')
    assert outputs['NamePrefix'] == PREFIX
    role = outputs['ParticipantViewerRoleArn'].split(':role/', 1)[1]
    # CFN roles are legacy aggregate identities, not native IAM role rows.
    refused(lambda: team.iam.get_role(RoleName=role), 'NoSuchEntity', 404)
    participant = team.participant(role)
    limited = team.client('ssm', participant)
    limited_cf = team.client('cloudformation', participant)
    refused(lambda: limited_cf.describe_stacks(StackName=STACK), 'AccessDenied', 403)
    refused(lambda: limited.get_parameter(Name=PRIVATE), 'AccessDeniedException', 403)
    if enabled:
        item = team.ssm.get_parameter(Name=ITEM)['Parameter']['Value']
        assert json.loads(item) == {'key': 3, 'receipt': seed}
        assert outputs['CoordinationPrivateItem'] == item  # operator-only raw output, NOT NoEcho secrecy
        assert outputs['CoordinationParameterName'] == ITEM
        assert 'systems-manager/parameters/' in outputs['CoordinationParameterConsoleUrl']
        assert limited.get_parameter(Name=ITEM)['Parameter']['Value'] == item
        listing = limited.describe_parameters()['Parameters']
        assert ITEM in {v['Name'] for v in listing} and all('Value' not in v for v in listing)
    else:
        assert not {'CoordinationPrivateItem', 'CoordinationParameterName',
                    'CoordinationParameterConsoleUrl'} & set(outputs)
        refused(lambda: team.ssm.get_parameter(Name=ITEM), 'ParameterNotFound', 400)
        refused(lambda: limited.get_parameter(Name=ITEM), 'AccessDeniedException', 403)
    # Dummy AWS-managed sign-in/trust metadata must never become real IAM grants.
    refused(lambda: limited.put_parameter(Name=ITEM + '-other', Type='String', Value='dummy'),
            'AccessDeniedException', 403)
    return record['StackId'], participant


def cleanup(team):
    team.cf.delete_stack(StackName=STACK)
    assert team.cf.describe_stacks()['Stacks'] == []
    refused(lambda: team.ssm.get_parameter(Name=ITEM), 'ParameterNotFound', 400)
    assert team.ssm.get_parameter(Name=PRIVATE)['Parameter']['Value'] == 'private-local-dummy'
    with contextlib.closing(sqlite3.connect(team.database)) as db:
        assert db.execute('SELECT count(*) FROM aws_cfn_stacks').fetchone()[0] == 0
        assert db.execute('SELECT count(*) FROM aws_cfn_ownership').fetchone()[0] == 0
        assert db.execute('SELECT count(*) FROM identity_policies WHERE name LIKE ?',
                          (STACK + '-ParticipantViewerRole-%',)).fetchone()[0] == 0


def run():
    raw = FIXTURE.read_bytes()
    assert hashlib.sha256(raw).hexdigest() == FIXTURE_SHA
    template = raw.decode()
    all_counts = []
    with tempfile.TemporaryDirectory(prefix='crypto-cfn-two-clis-') as directory:
        databases = [Path(directory) / 'one.db', Path(directory) / 'two.db']
        with running(databases[0], authenticated=True) as (one, token_one), \
             running(databases[1], authenticated=True) as (two, token_two):
            with contextlib.ExitStack() as client_cleanup:
                teams = []
                for endpoint, token, database in [(one, token_one, databases[0]),
                                                  (two, token_two, databases[1])]:
                    team = Team(endpoint, token, database)
                    client_cleanup.callback(team.close)
                    teams.append(team)
                assert token_one != token_two
                for team in teams:
                    team.ssm.put_parameter(Name=PRIVATE, Type='String', Value='private-local-dummy')
                for index, team in enumerate(teams):
                    # Parameter validation occurs before any persisted record/role/claim.
                    before = frame(team.database)
                    invalid = parameters(('A' if index == 0 else 'B') * 31, False)
                    refused(lambda: team.cf.create_stack(StackName=STACK, TemplateBody=template,
                        Parameters=invalid, Capabilities=['CAPABILITY_IAM']), 'ValidationError', 400)
                    assert frame(team.database) == before
                    team.cf.create_stack(StackName=STACK, TemplateBody=template,
                        Parameters=parameters(('A' if index == 0 else 'B') * 32, False),
                        Capabilities=['CAPABILITY_IAM'])
                    inspect(team, ('A' if index == 0 else 'B') * 32, False)
                    cleanup(team)
                # The selected role is inserted before a late SSM name collision;
                # a real failed create must leave the entire graph unchanged.
                peer_before = frame(teams[1].database)
                teams[0].ssm.put_parameter(Name=ITEM, Type='String', Value='preexisting-dummy')
                before = frame(teams[0].database)
                refused(lambda: teams[0].cf.create_stack(StackName=STACK, TemplateBody=template,
                    Parameters=parameters('A' * 32, True), Capabilities=['CAPABILITY_IAM']),
                    'AlreadyExistsException', 400)
                assert frame(teams[0].database) == before
                assert frame(teams[1].database) == peer_before
                teams[0].ssm.delete_parameter(Name=ITEM)  # repair only the test-owned collision
                saved = []
                for index, team in enumerate(teams):
                    team.cf.create_stack(StackName=STACK, TemplateBody=template,
                        Parameters=parameters(('A' if index == 0 else 'B') * 32, True),
                        Capabilities=['CAPABILITY_IAM'])
                    saved.append(inspect(team, ('A' if index == 0 else 'B') * 32, True))
                assert saved[0][0] != saved[1][0]
                peer_before = frame(teams[1].database)
                teams[1].wire('/', token=token_one, form={'Action': 'DescribeStacks',
                    'Version': '2010-05-15', 'StackName': STACK}, status=401)
                teams[1].wire('/', token=saved[0][1], form={'Action': 'DescribeStacks',
                    'Version': '2010-05-15', 'StackName': STACK}, status=401)
                assert frame(teams[1].database) == peer_before
                cleanup(teams[1])
                assert json.loads(teams[0].ssm.get_parameter(Name=ITEM)['Parameter']['Value'])['receipt'] == 'A' * 32
                old_participant = saved[0][1]
            all_counts.extend(team.counts for team in teams)
        # Actual CLI process replacement, same DB, fresh host token authority.
        with running(databases[0], authenticated=True) as (endpoint, token):
            with contextlib.ExitStack() as client_cleanup:
                team = Team(endpoint, token, databases[0])
                client_cleanup.callback(team.close)
                assert token != token_one
                assert described(team)[0]['StackId'] == saved[0][0]
                team.wire('/', token=old_participant, form={'Action': 'DescribeStacks',
                    'Version': '2010-05-15', 'StackName': STACK}, status=401)
                inspect(team, 'A' * 32, True)
                cleanup(team)
                team.ssm.delete_parameter(Name=PRIVATE)
                assert team.ssm.describe_parameters()['Parameters'] == []
            all_counts.append(team.counts)
        # Reopening the already cleaned peer must preserve its unrelated value.
        with running(databases[1], authenticated=True) as (endpoint, token):
            with contextlib.ExitStack() as client_cleanup:
                team = Team(endpoint, token, databases[1])
                client_cleanup.callback(team.close)
                assert team.cf.describe_stacks()['Stacks'] == []
                assert team.ssm.get_parameter(Name=PRIVATE)['Parameter']['Value'] == 'private-local-dummy'
                team.ssm.delete_parameter(Name=PRIVATE)
                assert team.ssm.describe_parameters()['Parameters'] == []
                team.wire('/_simulator/health')
            all_counts.append(team.counts)
    for counts in all_counts:
        assert counts['sdkCall'] > 0
        assert counts['sdkCall'] == counts['sdkSend'] == counts['sdkResponse'] == counts['sdkParsed']
        assert counts['nativeSend'] > 0 and counts['nativeSend'] == counts['nativeResponse']
    with BINARY.open('rb') as binary:
        binary_sha = hashlib.file_digest(binary, 'sha256').hexdigest()
    result = {'binarySHA256': binary_sha, 'originalTemplateSHA256': FIXTURE_SHA, 'cliIncarnations': 4,
              'twoDBTokenAuthorities': True, 'defaultAndOptionalProvisioning': True,
              'actualEffectiveSSMPermissions': True, 'participantStackOutputsDenied': True,
              'nativeIAMVisibility': 'CFN legacy identities are not native IAM roles',
              'invalidAndLateConflictFullFrameRollback': True,
              'restartAndPeerPreservation': True, 'cleanup': True,
              'transportCounts': all_counts, 'hostCoordinationAndGrading': 'NOT EXECUTED',
              'NoEchoOrSecretStorageGuarantee': False}
    REPORTS.mkdir(parents=True, exist_ok=True)
    (REPORTS / 'cloudformation-crypto-sdk.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result))


if __name__ == '__main__':
    run()
