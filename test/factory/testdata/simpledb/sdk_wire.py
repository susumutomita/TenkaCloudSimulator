#!/usr/bin/env python3
"""Separate first-acceptance SDK wire surface. No cloud, install, or factory registration.
Exit 0=all required cases passed, 1=product contract failure, 2=harness failure.
"""
import argparse
import contextlib
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parent
PINS = {'boto3': '1.42.0', 'botocore': '1.41.6'}
REQUIRED = ['sdk-domain-items', 'sdk-restart-cascade', 'sdk-list-pages', 'sdk-negative-cases']
class ProductFailure(AssertionError): pass

def require(condition, message):
    if not condition:
        raise ProductFailure(message)

def preflight():
    # This happens before imports/clients; no ambient profile or metadata lookup.
    for key in list(os.environ):
        if key.startswith(('AWS_', 'BOTO_')) or key.upper().endswith('_PROXY'):
            os.environ.pop(key, None)
    os.environ.update(AWS_CONFIG_FILE='/dev/null', AWS_SHARED_CREDENTIALS_FILE='/dev/null', AWS_EC2_METADATA_DISABLED='true')
    for package, version in PINS.items():
        actual = importlib.metadata.version(package)
        if actual != version:
            raise RuntimeError(f'fixed SDK dependency mismatch: {package}={actual}, required={version}')
    import boto3
    from botocore.config import Config
    from botocore.exceptions import ClientError, BotoCoreError
    from botocore.parsers import ResponseParserError
    return boto3, Config, ClientError, ResponseParserError, BotoCoreError

@contextlib.contextmanager
def running(binary, db, report):
    with tempfile.TemporaryDirectory(prefix='simpledb-sdk-cli-') as directory:
        token_file = Path(directory) / 'operator-token'
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        endpoint = f'http://127.0.0.1:{port}'
        args = [str(binary), '--listen', f'127.0.0.1:{port}', '--state', str(db), '--token-file', str(token_file)]
        process = subprocess.Popen(args, cwd=directory, env={'PATH': '/nonexistent'}, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        primary_error = None
        try:
            deadline = time.monotonic() + 20
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            while True:
                if process.poll() is not None:
                    raise RuntimeError('CLI exited before readiness')
                if time.monotonic() >= deadline:
                    raise RuntimeError('CLI readiness timed out')
                try:
                    token = token_file.read_text().strip()
                    request = urllib.request.Request(endpoint + '/_simulator/health', headers={'X-Simulator-Token': token})
                    with opener.open(request, timeout=1) as response:
                        if response.status == 200:
                            break
                except (OSError, ValueError):
                    time.sleep(.05)
            yield endpoint, token
        except BaseException as error:
            primary_error = error
            raise
        finally:
            try:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=3)
                    raise RuntimeError('CLI cleanup timed out')
                if process.returncode != 0:
                    raise RuntimeError(f'CLI cleanup exit={process.returncode}')
            except Exception as cleanup_error:
                report.setdefault('cleanup_errors', []).append(f'{type(cleanup_error).__name__}: {cleanup_error}')
                if primary_error is None:
                    raise

def client_for(boto3, Config, endpoint, token):
    expected = urllib.parse.urlsplit(endpoint)
    client = boto3.client('sdb', endpoint_url=endpoint, region_name='us-east-1', aws_access_key_id='TCSIMEXAMPLE', aws_secret_access_key='simulator-only', config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=2, read_timeout=3))
    if client.meta.service_model.metadata.get('signatureVersion') != 'v2':
        raise RuntimeError('fixed SimpleDB model is not the expected SigV2 model')
    def headers(request, **kwargs):
        request.headers['X-Simulator-Token'] = token
        request.headers['X-Simulator-Service'] = 'sdb'
        request.headers['X-Simulator-Region'] = 'us-east-1'
    def loopback_only(request, **kwargs):
        url = urllib.parse.urlsplit(request.url)
        if (url.scheme, url.hostname, url.port) != ('http', '127.0.0.1', expected.port):
            raise RuntimeError('non-owned or non-loopback SDK request refused')
    client.meta.events.register('before-sign.sdb', headers)
    client.meta.events.register('before-send.sdb', loopback_only)
    return client

def decoded_attributes(out):
    require(isinstance(out, dict), 'malformed product response object')
    values = out.get('Attributes', [])
    require(isinstance(values, list), 'malformed product Attributes list')
    for attribute in values:
        require(isinstance(attribute, dict) and isinstance(attribute.get('Name'), str) and isinstance(attribute.get('Value'), str), 'product attribute missing Name/Value string')
    return sorted((a['Name'], a['Value']) for a in values)

def attributes(client, f, call):
    return decoded_attributes(call(client, 'get_attributes', DomainName=f['domain'], ItemName=f['item'], ConsistentRead=True))

def execute(binary, report, sdk):
    boto3, Config, ClientError, ResponseParserError, BotoCoreError = sdk
    f = json.loads((ROOT / 'oracle/fixtures.json').read_text())
    expected = sorted((a['Name'], a['Value']) for a in f['attributes'])
    def call(client, op, **kwargs):
        try:
            out = getattr(client, op)(**kwargs)
            require(isinstance(out, dict), f'{op}: malformed SDK response object')
            metadata = out.get('ResponseMetadata')
            require(isinstance(metadata, dict) and metadata.get('HTTPStatusCode') == 200, f'{op}: missing successful response metadata')
            return out
        except ClientError as error:
            raise ProductFailure(f'{op}: unexpected SDK error code={error.response["Error"]["Code"]}, HTTP={error.response["ResponseMetadata"]["HTTPStatusCode"]}') from error
        except ResponseParserError as error:
            raise ProductFailure(f'{op}: malformed product wire response after owned CLI readiness') from error
        except BotoCoreError as error:
            raise RuntimeError(f'POST_READINESS_TRANSPORT_UNRESOLVED {op}: {type(error).__name__}') from error
    def expect_error(client, op, code, **kwargs):
        try:
            getattr(client, op)(**kwargs)
        except ClientError as error:
            require(error.response['Error']['Code'] == code, f'{op}: expected error {code}')
            require(error.response['ResponseMetadata']['HTTPStatusCode'] == 400, f'{op}: expected HTTP400')
        except ResponseParserError as error:
            raise ProductFailure(f'{op}: malformed product error wire response') from error
        except BotoCoreError as error:
            raise RuntimeError(f'POST_READINESS_TRANSPORT_UNRESOLVED {op}: {type(error).__name__}') from error
        else:
            raise ProductFailure(f'{op}: expected failure, got success')
    with tempfile.TemporaryDirectory(prefix='simpledb-sdk-state-') as state:
        db = Path(state) / 'state.db'
        with running(binary, db, report) as (endpoint, token):
            with contextlib.closing(client_for(boto3, Config, endpoint, token)) as client:
                call(client, 'create_domain', DomainName=f['domain'])
                call(client, 'create_domain', DomainName=f['domain'])
                require(call(client, 'list_domains').get('DomainNames', []) == [f['domain']], 'idempotent create/list')
                for _ in range(2):
                    call(client, 'put_attributes', DomainName=f['domain'], ItemName=f['item'], Attributes=f['attributes'])
                require(attributes(client, f, call) == expected, 'Put/Get duplicate elimination and XML escaping')
                call(client, 'put_attributes', DomainName=f['domain'], ItemName=f['item'], Attributes=[{'Name': 'colour', 'Value': 'red', 'Replace': True}])
                expected = sorted((a['Name'], a['Value']) for a in f['replaced_attributes'])
                require(attributes(client, f, call) == expected, 'SDK Replace did not replace the value set')
                filtered = call(client, 'get_attributes', DomainName=f['domain'], ItemName=f['item'], AttributeNames=['colour'], ConsistentRead=True)
                require(decoded_attributes(filtered) == [('colour', 'red')], 'SDK attribute-name filter')
                report['passed_cases'].append(REQUIRED[0])
        with running(binary, db, report) as (endpoint, token):
            with contextlib.closing(client_for(boto3, Config, endpoint, token)) as client:
                require(attributes(client, f, call) == expected, 'restart lost attributes')
                call(client, 'delete_domain', DomainName=f['domain'])
                call(client, 'delete_domain', DomainName=f['domain'])
        with running(binary, db, report) as (endpoint, token):
            with contextlib.closing(client_for(boto3, Config, endpoint, token)) as client:
                require(call(client, 'list_domains').get('DomainNames', []) == [], 'deleted domain reappeared')
                expect_error(client, 'get_attributes', 'NoSuchDomain', DomainName=f['domain'], ItemName=f['item'])
                call(client, 'create_domain', DomainName=f['domain'])
                require(attributes(client, f, call) == [], 'recreated domain retained old item state')
                call(client, 'delete_domain', DomainName=f['domain'])
                report['passed_cases'].append(REQUIRED[1])
                for domain in f['domains']:
                    call(client, 'create_domain', DomainName=domain)
                for params in ({}, {'MaxNumberOfDomains': 100}):
                    out = call(client, 'list_domains', **params)
                    require(sorted(out.get('DomainNames', [])) == sorted(f['domains']) and not out.get('NextToken'), 'default/max list page')
                seen, tokens, next_token = set(), set(), None
                for _ in range(4):
                    params = {'MaxNumberOfDomains': 1}
                    if next_token:
                        params['NextToken'] = next_token
                    out = call(client, 'list_domains', **params)
                    names = out.get('DomainNames', [])
                    require(len(names) == 1 and not seen.intersection(names), 'page bound/duplicate')
                    seen.update(names)
                    next_token = out.get('NextToken')
                    if not next_token:
                        break
                    require(next_token not in tokens, 'pagination token failed to progress')
                    tokens.add(next_token)
                require(not next_token and seen == set(f['domains']), 'incomplete pagination')
                report['passed_cases'].append(REQUIRED[2])
                for bound in (0, 101):
                    expect_error(client, 'list_domains', 'InvalidParameterValue', MaxNumberOfDomains=bound)
                expect_error(client, 'list_domains', 'InvalidNextToken', NextToken='not-a-issued-token')
                expect_error(client, 'get_attributes', 'NoSuchDomain', DomainName='factory-absent', ItemName='item')
                for domain in f['domains']:
                    call(client, 'delete_domain', DomainName=domain)
                require(call(client, 'list_domains').get('DomainNames', []) == [], 'final cleanup left domains')
                report['passed_cases'].append(REQUIRED[3])

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', type=Path)
    parser.add_argument('--binary-sha256')
    parser.add_argument('--report', required=True, type=Path)
    parser.add_argument('--preflight-only', action='store_true')
    args = parser.parse_args()
    report = {'surface': 'sdk-wire-real-local-cli', 'required_cases': REQUIRED, 'passed_cases': [], 'status': 'HARNESS_ERROR', 'sdk_pins': PINS, 'qualification': 'test-writer-only snapshot; not registered or accepted factory evidence'}
    code = 2
    try:
        sdk = preflight()
        if args.preflight_only:
            report['status'] = 'PREFLIGHT_ONLY_NOT_ACCEPTANCE'
            code = 0
        else:
            if not args.binary or not args.binary.is_absolute() or not args.binary.is_file() or not args.binary_sha256:
                raise RuntimeError('explicit absolute built CLI and SHA256 required')
            actual = hashlib.sha256(args.binary.read_bytes()).hexdigest()
            if actual != args.binary_sha256:
                raise RuntimeError('CLI hash mismatch')
            report['binary_sha256'] = actual
            execute(args.binary, report, sdk)
            require(report['passed_cases'] == REQUIRED, 'required SDK cases missing')
            report['status'], code = 'PASS', 0
    except ProductFailure as error:
        report['status'], report['reason'], code = 'PRODUCT_CONTRACT_RED', str(error), 1
    except Exception as error:
        report['reason'] = f'{type(error).__name__}: {error}'
    if report.get('cleanup_errors'):
        report['cleanup_status'] = 'FAILED'
        code = 2  # Preserve any primary product failure and also reject cleanup.
    args.report.parent.mkdir(parents=True, exist_ok=True)
    args.report.write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps({'status': report['status'], 'passed_cases': report['passed_cases'], 'exit': code}))
    return code

if __name__ == '__main__':
    sys.exit(main())
