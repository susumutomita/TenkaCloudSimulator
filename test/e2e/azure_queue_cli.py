#!/usr/bin/env python3
"""Explicit opt-in local CLI/SDK runner. No install, build, fallback or skip.

Boundary unit tests need only stdlib. Actual execution requires the reviewed
azure-storage-queue 12.18.1 and existing pinned SDK dependencies plus a built
simulator. Run with the separately recorded build digest:

    python3 test/e2e/azure_queue_cli.py --binary /absolute/path/to/simulator \
        --binary-sha256 "$VERIFIED_BUILD_SHA256"

Only two simultaneous local processes are used. The on-disk owner is restarted
while its peer stays active. Install the hash-pinned Queue wheel beforehand in
an approved isolated environment. The runner does not provision dependencies.
A unit-test pass is not a real CLI/SDK receipt.
"""
import argparse
from contextlib import contextmanager
from dataclasses import dataclass
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit
from azure_queue_official import PIN, VERSION, field, run_lifecycle

OPERATIONS = {'CreateQueue','DeleteQueue','PutMessage','GetMessages','PeekMessages','UpdateMessage','DeleteMessage','ClearMessages'}
PREFIX = '/_simulator/providers/azure-queue/tenkacloudlocal'

class Blocked(RuntimeError): pass

def check_environment(env):
    # Do not silently remove a user's proxy or interpret wildcard bypass rules.
    if any(v and k.lower().endswith('_proxy') for k,v in env.items()):
        raise Blocked('Proxy configuration present; explicit clean verification environment required')

def check_url(url, port):
    try: p=urlsplit(url)
    except ValueError: raise Blocked('Invalid request URL') from None
    if not (type(port) is int and 0 < port < 65536 and p.scheme=='http'
            and p.netloc==f'127.0.0.1:{port}' and not p.fragment
            and not any(c in url for c in '\r\n\\')):
        raise Blocked('Request destination is not the assigned numeric loopback endpoint')

def guarded_send(send, request, port, **kwargs):
    check_url(request.url,port)
    if kwargs.get('proxies'): raise Blocked('Transport proxy forbidden')
    kwargs['allow_redirects']=False
    response=send(request,**kwargs)
    if 300 <= response.status_code < 400:
        response.close()
        raise Blocked('Redirect rejected before follow-up request')
    return response

def check_capability(caps):
    c=caps.get('cloudStorage',{}).get('azure-queue',{})
    operations=c.get('operations',[])
    if (c.get('endpoint')!=PREFIX or c.get('apiVersion')!=VERSION
            or not isinstance(operations,list) or len(operations) != len(OPERATIONS)
            or set(operations)!=OPERATIONS):
        raise Blocked('Queue capability does not match fixed lifecycle')

def stop_process(process):
    if process.poll() is None: process.terminate()
    try: code=process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.kill(); process.wait(timeout=5)
        raise Blocked('CLI required forced termination')
    if code!=0: raise Blocked('CLI did not exit successfully')

def finish(process,cleanup):
    try: stop_process(process)
    finally: cleanup()


@dataclass(frozen=True)
class VerifiedBinary:
    path: Path
    digest: str
    identity: tuple

    def recheck(self):
        if binary_identity(self.path.stat()) != self.identity:
            raise Blocked('Simulator binary changed after digest verification')


def binary_identity(stat):
    return (stat.st_dev, stat.st_ino, stat.st_size, stat.st_mode, stat.st_mtime_ns, stat.st_ctime_ns)


def verify_binary(binary, expected_digest):
    if not isinstance(expected_digest, str) or not re.fullmatch('[a-f0-9]{64}', expected_digest):
        raise Blocked('An independently recorded simulator SHA-256 is required')
    path = Path(binary).resolve(strict=True)
    if not path.is_file() or not os.access(path, os.X_OK):
        raise Blocked('Expected an executable simulator binary')
    with path.open('rb') as stream:
        identity = binary_identity(os.fstat(stream.fileno()))
        digest = hashlib.sha256()
        for chunk in iter(lambda: stream.read(256 * 1024), b''):
            digest.update(chunk)
        if binary_identity(os.fstat(stream.fileno())) != identity or digest.hexdigest() != expected_digest:
            raise Blocked('Simulator executable digest does not match')
    verified = VerifiedBinary(path, digest.hexdigest(), identity)
    verified.recheck()
    return verified


@dataclass(frozen=True, repr=False)
class LocalInstance:
    port: int
    token: str

    @property
    def endpoint(self):
        return f'http://127.0.0.1:{self.port}'


@contextmanager
def running_cli(binary, directory):
    directory.mkdir(mode=0o700, exist_ok=True)
    log_path = directory / 'cli.log'
    token_path = directory / 'token'
    binary.recheck()
    with log_path.open('w+') as log:
        process = subprocess.Popen([str(binary.path), '--listen', '127.0.0.1:0',
            '--state', str(directory / 'state.db'), '--token-file', str(token_path)],
            cwd=directory, stdin=subprocess.DEVNULL, stdout=log, stderr=log,
            env={k: v for k, v in os.environ.items() if k in ('PATH', 'TMPDIR', 'SYSTEMROOT')})
        try:
            # On Linux confirm the owned live process maps the verified executable.
            # Elsewhere bind each launch to the same unchanged digest-checked file.
            if sys.platform == 'linux':
                runtime = Path(f'/proc/{process.pid}/exe').stat()
                if (runtime.st_dev, runtime.st_ino) != binary.identity[:2]:
                    raise Blocked('The launched executable differs from the verified binary')
            binary.recheck()
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise Blocked('CLI exited during startup')
                log.seek(0)
                output = log.read(32769)
                if len(output) > 32768:
                    raise Blocked('Unexpected volume of CLI startup output')
                match = re.search(r'^AWS simulation endpoint http://127\.0\.0\.1:([1-9][0-9]{0,4})$', output, re.M)
                if match:
                    break
                time.sleep(.05)
            else:
                raise Blocked('CLI startup timeout')
            port = int(match.group(1))
            if token_path.is_symlink() or not token_path.is_file():
                raise Blocked('Invalid local operator token file')
            attrs = token_path.stat()
            if attrs.st_mode & 0o077 or not 64 <= attrs.st_size <= 128:
                raise Blocked('Invalid or nonprivate local operator token file')
            token = token_path.read_text().strip()
            if not re.fullmatch('[a-f0-9]{64}', token):
                raise Blocked('Invalid local operator token')
            instance = LocalInstance(port, token)
            check_url(instance.endpoint, port)
            yield instance
        finally:
            stop_process(process)


QUEUE_NAME = 'sdk-queue'
CAPS = '/_simulator/v1/capabilities'
OWNER_TEXT = 'first local owner & 再起動'
UPDATED_TEXT = 'restarted local owner & 更新'
PEER_TEXT = 'independent local peer & 保持'


def distinct_instances(owner, peer):
    if owner.port == peer.port or owner.token == peer.token:
        raise Blocked('Expected two live endpoints and separate host-issued operator tokens')


def expect_sdk_status(error_type, status, operation):
    try:
        operation()
    except error_type as error:
        actual = getattr(error, 'status_code', None)
        if actual is None:
            actual = getattr(getattr(error, 'response', None), 'status_code', None)
        if actual != status:
            raise Blocked(f'Expected local SDK HTTP {status}') from None
    else:
        raise Blocked(f'Expected local SDK HTTP {status}; the operation succeeded')


def peek_exact(client, ident, content, count=0):
    messages = client.peek_messages(max_messages=2)
    if len(messages) != 1 or field(messages[0], 'id') != ident \
            or field(messages[0], 'content') != content or field(messages[0], 'dequeue_count') != count:
        raise Blocked('The local queue state does not match its isolated owner')


def empty(client):
    if client.peek_messages(max_messages=2):
        raise Blocked('Expected no visible messages in the local queue')


def run_two_instances(launch, connect, sdk_error):
    """Drive the real SDK from run(); offline unit doubles only test this oracle."""
    def absent(client):
        expect_sdk_status(sdk_error, 404, lambda: client.peek_messages(max_messages=1))

    with launch('peer') as peer, connect(peer) as b:
        absent(b)
        b.create_queue()
        peer_id = field(b.send_message(PEER_TEXT), 'id')
        if not peer_id:
            raise Blocked('Peer send did not return a message id')
        peek_exact(b, peer_id, PEER_TEXT)

        with launch('owner') as owner, connect(owner) as a:
            distinct_instances(owner, peer)
            absent(a)  # The live peer already has the exact same queue name.
            a.create_queue()
            owner_id = field(a.send_message(OWNER_TEXT), 'id')
            if not owner_id or owner_id == peer_id:
                raise Blocked('Expected separately allocated message identities')
            peek_exact(a, owner_id, OWNER_TEXT)
            with connect(owner, peer.token) as wrong_owner, connect(peer, owner.token) as wrong_peer:
                expect_sdk_status(sdk_error, 401, lambda: wrong_owner.peek_messages(max_messages=1))
                expect_sdk_status(sdk_error, 401, wrong_peer.clear_messages)
            peek_exact(a, owner_id, OWNER_TEXT)
            peek_exact(b, peer_id, PEER_TEXT)
            lease = a.receive_message(visibility_timeout=300)
            if lease is None or field(lease, 'id') != owner_id or field(lease, 'content') != OWNER_TEXT \
                    or field(lease, 'dequeue_count') != 1 or not field(lease, 'pop_receipt'):
                raise Blocked('Owner message was not reserved')
            previous = field(lease, 'pop_receipt')
            empty(a)

        # The first process has gracefully exited. The peer remains online and
        # independent; reopen only the owner's exact state and token files.
        peek_exact(b, peer_id, PEER_TEXT)
        with launch('owner') as reopened, connect(reopened) as a:
            distinct_instances(reopened, peer)
            if reopened.token != owner.token:
                raise Blocked('Restart did not preserve the host-owned operator token')
            empty(a)
            updated = a.update_message(owner_id, pop_receipt=previous, visibility_timeout=0, content=UPDATED_TEXT)
            receipt = field(updated, 'pop_receipt')
            if not receipt or receipt == previous:
                raise Blocked('Restart/update did not preserve and rotate the current receipt')
            peek_exact(a, owner_id, UPDATED_TEXT, count=1)
            a.delete_message(owner_id, pop_receipt=receipt)
            empty(a)
            a.delete_queue()
            absent(a)
            # Keep the existing eight-operation oracle unchanged on fresh state.
            result = run_lifecycle(a)
            absent(a)
            peek_exact(b, peer_id, PEER_TEXT)

        b.delete_queue()
        absent(b)
    return result


def run(binary, expected_digest=None):
    check_environment(os.environ)
    if importlib.metadata.version('azure-storage-queue') != PIN:
        raise Blocked('Official Queue SDK pin mismatch')
    binary = verify_binary(binary, expected_digest)
    # Lazy imports ensure offline boundary tests cannot load cloud clients.
    import requests
    from azure.core.exceptions import HttpResponseError
    from azure.core.pipeline.transport import RequestsTransport
    from azure.storage.queue import QueueClient

    @contextmanager
    def connect(instance, token=None):
        class LocalSession(requests.Session):
            def send(self, request, **kwargs):
                return guarded_send(super().send, request, instance.port, **kwargs)

        with LocalSession() as session:
            session.trust_env = False
            session.headers['X-Simulator-Token'] = instance.token if token is None else token
            if token is None:
                with session.get(instance.endpoint + CAPS, timeout=5) as response:
                    response.raise_for_status()
                    check_capability(response.json())
            transport = RequestsTransport(session=session, session_owner=False, use_env_settings=False)
            with QueueClient(account_url=instance.endpoint + PREFIX, queue_name=QUEUE_NAME,
                    credential=None, api_version=VERSION, retry_total=0, transport=transport,
                    connection_timeout=5, read_timeout=5) as client:
                yield client

    with tempfile.TemporaryDirectory(prefix='azure-queue-cli-') as temp:
        directory = Path(temp)
        result = run_two_instances(lambda name: running_cli(binary, directory / name), connect, HttpResponseError)
        binary.recheck()
    if directory.exists():
        raise Blocked('Temporary state cleanup incomplete')
    return dict(result, kind='real-local-cli-sdk', binary_sha256=binary.digest, sdk=PIN, api_version=VERSION,
                scope='two local processes, separate tokens/state, same queue name and owner restart',
                cross_token_refusal='passed', independent_peer='passed', restart='passed', cleanup='passed')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--binary-sha256', required=True, help='Trusted SHA-256 of the built simulator')
    args = parser.parse_args()
    try:
        result = run(args.binary, args.binary_sha256)
    except importlib.metadata.PackageNotFoundError:
        print(json.dumps({'status': 'blocked', 'stage': 'sdk-preflight', 'sdk': PIN}))
        return 1
    except Exception as exc:
        # Do not expose token-bearing SDK URLs/headers or exception payloads.
        print(json.dumps({'status': 'failed', 'error_type': type(exc).__name__}))
        return 1
    print(json.dumps(result))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
