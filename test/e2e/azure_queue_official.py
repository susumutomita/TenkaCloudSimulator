#!/usr/bin/env python3
"""Test-first official SDK lifecycle oracle. No server is started or installed.

Use --capture to measure the installed SDK's prepared local URLs without network.
Actual two-CLI acceptance wiring is a separate root-owned integration step.
"""
import argparse
from base64 import b64encode
import importlib.metadata
from urllib.parse import urlsplit, parse_qs
import xml.etree.ElementTree as ET

PIN = '12.18.1'
VERSION = '2023-11-03'
PREFIX = '/_simulator/providers/azure-queue/localaccount'
EXPECTED = {'create', 'send', 'peek', 'receive', 'update', 'delete', 'clear', 'delete_queue'}

class AcceptanceFailure(RuntimeError):
    pass

def require(value, reason):
    if not value:
        raise AcceptanceFailure(reason)

def field(message, name):
    return message[name] if isinstance(message, dict) else getattr(message, name)

def run_lifecycle(client):
    """Same oracle must run through real SDK client; fakes test the detector only."""
    seen = set()
    def call(operation, method, *args, **kwargs):
        result = getattr(client, method)(*args, **kwargs)
        seen.add(operation)
        return result
    call('create', 'create_queue')
    sent = call('send', 'send_message', 'native & persistent')
    require(field(sent, 'id'), 'missing message ID')
    peek = call('peek', 'peek_messages', max_messages=1)
    require(len(peek) == 1 and field(peek[0], 'content') == 'native & persistent', 'peek content')
    require(field(peek[0], 'dequeue_count') == 0, 'peek count must be explicit zero')
    got = call('receive', 'receive_message', visibility_timeout=30)
    require(got is not None and field(got, 'id') == field(sent, 'id'), 'receive identity')
    require(field(got, 'dequeue_count') == 1 and field(got, 'pop_receipt'), 'receive receipt/count')
    require(not call('peek', 'peek_messages', max_messages=1), 'reserved message visible')
    previous_receipt = field(got, 'pop_receipt')
    updated = call('update', 'update_message', got, visibility_timeout=0)
    require(field(updated, 'pop_receipt') and field(updated, 'pop_receipt') != previous_receipt, 'update receipt')
    call('delete', 'delete_message', field(got, 'id'), pop_receipt=field(updated, 'pop_receipt'))
    require(not call('peek', 'peek_messages', max_messages=1), 'DeleteMessage no-op before ClearMessages')
    require(call('receive', 'receive_message', visibility_timeout=30) is None, 'deleted message received')
    call('send', 'send_message', 'clear separately')
    call('clear', 'clear_messages')
    require(not call('peek', 'peek_messages', max_messages=1), 'ClearMessages no-op')
    call('delete_queue', 'delete_queue')
    require(seen == EXPECTED, 'operation set incomplete')
    return {'status': 'passed', 'operations': sorted(seen), 'scope': 'client lifecycle only; not two-team/restart/auth proof'}

def local_sdk_credential(account):
    # QueueClient requires a signer even on loopback. The simulator uses its
    # own token for access. This fixed value is public fixture data.
    from azure.core.credentials import AzureNamedKeyCredential
    return AzureNamedKeyCredential(account, b64encode(b'local queue SDK fixture').decode('ascii'))


class Captured(Exception):
    pass

def validate_capture(captures):
    """Validate actual SDK captures; fixture controls do not prove SDK behavior."""
    expected = [
        ('PUT', '', {}, None),
        ('POST', '/messages', {'visibilitytimeout':['0'], 'messagettl':['60']}, 'synthetic & checked'),
        ('GET', '/messages', {'peekonly':['true'], 'numofmessages':['1']}, None),
        ('GET', '/messages', {'visibilitytimeout':['30'], 'numofmessages':['1']}, None),
        ('PUT', '/messages/synthetic-id', {'popreceipt':['receipt+/='], 'visibilitytimeout':['0']}, 'changed & checked'),
        ('DELETE', '/messages/synthetic-id', {'popreceipt':['receipt+/=']}, None),
        ('DELETE', '/messages', {}, None), ('DELETE', '', {}, None),
    ]
    require(len(captures) == len(expected), 'SDK operation count differs')
    for (method, path, query, body), (want_method, suffix, want_query, want_text) in zip(captures, expected):
        require(method == want_method and path == PREFIX + '/sdk-queue' + suffix, 'SDK operation target differs')
        require(parse_qs(query, keep_blank_values=True, strict_parsing=True) == want_query, 'SDK operation query differs')
        if want_text is None:
            require(body in (None, '', b''), 'unexpected operation body')
        else:
            try:
                root = ET.fromstring(body)
            except (ET.ParseError, TypeError) as exc:
                raise AcceptanceFailure('SDK operation XML invalid') from exc
            require(root.tag == 'QueueMessage' and not root.attrib and len(root) == 1,
                    'SDK operation XML envelope differs')
            child = root[0]
            require(child.tag == 'MessageText' and not child.attrib and len(child) == 0
                    and child.text == want_text, 'SDK operation XML payload differs')


def capture_sdk_paths():
    """Actual official SDK request preparation, never a mocked client success."""
    require(importlib.metadata.version('azure-storage-queue') == PIN, 'SDK pin differs')
    from azure.storage.queue import QueueClient
    captures = []
    def hook(pipeline_request):
        request = pipeline_request.http_request
        parsed = urlsplit(request.url)
        require(parsed.scheme == 'http' and parsed.netloc == '127.0.0.1:1', 'nonlocal SDK endpoint')
        require(parsed.path.startswith(PREFIX + '/sdk-queue'), 'SDK rewrote account URL path')
        require(request.headers.get('x-ms-version') == VERSION, 'SDK API version differs')
        captures.append((request.method, parsed.path, parsed.query, request.body))
        # Stop before transport: no socket or cloud access is performed.
        raise Captured()
    with QueueClient(account_url='http://127.0.0.1:1' + PREFIX,
                     queue_name='sdk-queue', credential=local_sdk_credential('localaccount'), api_version=VERSION,
                     raw_request_hook=hook, retry_total=0) as client:
        operations = [lambda: client.create_queue(), lambda: client.send_message('synthetic & checked', visibility_timeout=0, time_to_live=60),
                      lambda: client.peek_messages(max_messages=1),
                      lambda: client.receive_message(visibility_timeout=30),
                      lambda: client.update_message('synthetic-id', pop_receipt='receipt+/=', visibility_timeout=0, content='changed & checked'),
                      lambda: client.delete_message('synthetic-id', pop_receipt='receipt+/='),
                      lambda: client.clear_messages(), lambda: client.delete_queue()]
        for operation in operations:
            before = len(captures)
            try:
                operation()
            except Captured:
                pass
            require(len(captures) == before + 1, 'missing or repeated SDK prepared request')
    validate_capture(captures)
    return captures

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--capture', action='store_true', required=True)
    parser.parse_args()
    # Missing SDK is a hard error, never skip or silently replace with a fake.
    print(capture_sdk_paths())
