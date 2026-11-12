"""Built binary + official boto3 Bedrock Runtime acceptance against a fake Ollama.

The fake Ollama HTTP server below is a synthetic test double of the backend
boundary. Nothing here contacts AWS, downloads a model, runs a real model,
uses Apple hardware or runs Claude Code. Prompts are synthetic; reports omit
tokens, prompts and replies.
"""
import contextlib
import http.server
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

os.environ.update(AWS_CONFIG_FILE='/dev/null', AWS_SHARED_CREDENTIALS_FILE='/dev/null',
                  AWS_EC2_METADATA_DISABLED='true', AWS_DEFAULT_REGION='us-east-1')
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

BINARY = Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve()
REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
MODEL = 'anthropic.claude-3-haiku-20240307-v1:0'
ABSENT = 'amazon.nova-micro-v1:0'
PROMPT = 'synthetic-e2e-prompt-5d1c'
REPLY = 'synthetic-e2e-reply-9b7a'


class FakeOllama:
    """Answers POST /api/chat for synthetic:latest only and records requests."""

    def __init__(self):
        self.requests = []
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                owner.requests.append((self.path, body))
                if body.get('model') != 'synthetic:latest':
                    return self.reply(404, {'error': 'model not found, try pulling it first'})
                text = REPLY + ':' + body['messages'][-1]['content']
                self.reply(200, {'model': body['model'], 'message': {'role': 'assistant', 'content': text},
                                 'done': True, 'done_reason': 'stop', 'prompt_eval_count': 6, 'eval_count': 4})

            def reply(self, status, value):
                data = json.dumps(value).encode()
                self.send_response(status)
                self.send_header('Content-Type', 'application/json; charset=utf-8')
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *args):
                pass

        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.url = 'http://127.0.0.1:%d' % self.server.server_port
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


def free_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


@contextlib.contextmanager
def simulator(directory, extra):
    port = free_port()
    endpoint = 'http://127.0.0.1:%d' % port
    token_file = Path(directory) / 'operator-token'
    args = [str(BINARY), '--listen', '127.0.0.1:%d' % port, '--state', str(Path(directory) / 'state.db'), '--token-file', str(token_file)] + extra
    process = subprocess.Popen(args, cwd=directory, env={**os.environ, 'PATH': '/nonexistent'}, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        deadline = time.monotonic() + 25
        while True:
            if process.poll() is not None:
                raise RuntimeError(process.communicate())
            try:
                token = token_file.read_text().strip() if token_file.exists() else ''
                request = urllib.request.Request(endpoint + '/_simulator/health', headers={'X-Simulator-Token': token})
                with urllib.request.urlopen(request, timeout=1) as response:
                    if response.status == 200:
                        break
            except OSError:
                if time.monotonic() > deadline:
                    raise TimeoutError('simulator did not become ready')
                time.sleep(0.05)
        yield endpoint, token
    finally:
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
        stdout, stderr = process.communicate(timeout=10)
        assert process.returncode == 0, (process.returncode, stderr)
        assert PROMPT not in stdout + stderr and REPLY not in stdout + stderr, 'inference text was logged'


def refused(extra, expected):
    with tempfile.TemporaryDirectory(prefix='tcsim-bedrock-refused-') as directory:
        args = [str(BINARY), '--listen', '127.0.0.1:%d' % free_port(), '--state', str(Path(directory) / 'state.db'), '--token-file', str(Path(directory) / 'token')] + extra
        result = subprocess.run(args, cwd=directory, env={**os.environ, 'PATH': '/nonexistent'}, capture_output=True, text=True, timeout=20)
        assert result.returncode != 0 and expected in result.stderr, (extra, result.returncode, result.stderr)
        assert sorted(os.listdir(directory)) == [], 'refused startup created files'


def runtime_client(endpoint, token, region='us-east-1'):
    client = boto3.client('bedrock-runtime', endpoint_url=endpoint, region_name=region, aws_access_key_id='TCSIMEXAMPLE',
                          aws_secret_access_key='simulator-only', config=Config(retries={'max_attempts': 0}))
    client.meta.events.register('before-sign.bedrock-runtime', lambda request, **_: request.headers.add_header('X-Simulator-Token', token))
    return client


def control_client(endpoint, token):
    client = boto3.client('bedrock', endpoint_url=endpoint, region_name='us-east-1', aws_access_key_id='TCSIMEXAMPLE',
                          aws_secret_access_key='simulator-only', config=Config(retries={'max_attempts': 0}))
    client.meta.events.register('before-sign.bedrock', lambda request, **_: request.headers.add_header('X-Simulator-Token', token))
    return client


def error(call):
    try:
        call()
    except ClientError as failure:
        return failure.response['ResponseMetadata']['HTTPStatusCode'], failure.response['Error']['Code']
    raise AssertionError('call unexpectedly succeeded')


def wire(endpoint, token, path, value, method='POST'):
    request = urllib.request.Request(endpoint + path, data=json.dumps(value).encode(), method=method,
                                     headers={'X-Simulator-Token': token, 'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.loads(response.read() or b'{}')


def capabilities(endpoint, token):
    request = urllib.request.Request(endpoint + '/_simulator/v1/capabilities', headers={'X-Simulator-Token': token})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.loads(response.read())['services']['bedrock-runtime']


def converse(client, model=MODEL, text=PROMPT, **extra):
    return client.converse(modelId=model, messages=[{'role': 'user', 'content': [{'text': text}]}], **extra)


def main():
    assert BINARY.exists(), 'run make build first'
    report = {'backend': 'fake Ollama test double; no real model', 'checks': []}
    check = report['checks'].append

    refused(['--bedrock-runtime-backend', 'apple', '--bedrock-model', MODEL + '=x'], 'Apple Foundation Models backend is unavailable')
    refused(['--bedrock-runtime-backend', 'ollama', '--bedrock-ollama-url', 'http://localhost:11434', '--bedrock-model', MODEL + '=x'], 'numeric loopback')
    refused(['--unsafe-no-auth', '--bedrock-runtime-backend', 'ollama', '--bedrock-model', MODEL + '=x'], 'authenticated access')
    refused(['--bedrock-model', MODEL + '=x'], 'require --bedrock-runtime-backend')
    check('startup refusals: apple, host name URL, unauthenticated, orphan mapping')

    with tempfile.TemporaryDirectory(prefix='tcsim-bedrock-disabled-') as directory:
        with simulator(directory, []) as (endpoint, token):
            client = runtime_client(endpoint, token)
            assert error(lambda: converse(client)) == (501, 'UnknownOperationException')
            assert capabilities(endpoint, token)['enabled'] is False
    check('default process: inference disabled with UnknownOperationException 501')

    ollama = FakeOllama()
    try:
        with tempfile.TemporaryDirectory(prefix='tcsim-bedrock-') as directory:
            flags = ['--bedrock-runtime-backend', 'ollama', '--bedrock-ollama-url', ollama.url,
                     '--bedrock-model', MODEL + '=synthetic:latest', '--bedrock-model', ABSENT + '=absent:latest',
                     '--bedrock-runtime-timeout', '10s', '--bedrock-runtime-concurrency', '1']
            for attempt in ('first', 'restart'):
                with simulator(directory, flags) as (endpoint, token):
                    client = runtime_client(endpoint, token)
                    before = len(ollama.requests)
                    reply = converse(client, system=[{'text': 'synthetic system'}], inferenceConfig={'maxTokens': 16, 'temperature': 0.1})
                    assert reply['output']['message'] == {'role': 'assistant', 'content': [{'text': REPLY + ':' + PROMPT}]}
                    assert reply['stopReason'] == 'end_turn' and reply['usage'] == {'inputTokens': 6, 'outputTokens': 4, 'totalTokens': 10}
                    path, sent = ollama.requests[before]
                    assert path == '/api/chat' and sent == {'model': 'synthetic:latest', 'stream': False, 'options': {'num_predict': 16, 'temperature': 0.1},
                                                            'messages': [{'role': 'system', 'content': 'synthetic system'}, {'role': 'user', 'content': PROMPT}]}, sent
                    body = {'anthropic_version': 'bedrock-2023-05-31', 'max_tokens': 8, 'messages': [{'role': 'user', 'content': [{'type': 'text', 'text': PROMPT}]}]}
                    arn = 'arn:aws:bedrock:ap-northeast-1::foundation-model/' + MODEL
                    reply = runtime_client(endpoint, token, 'ap-northeast-1').invoke_model(modelId=arn, body=json.dumps(body), contentType='application/json', accept='application/json')
                    decoded = json.loads(reply['body'].read())
                    headers = reply['ResponseMetadata']['HTTPHeaders']
                    assert reply['contentType'] == 'application/json' and decoded['content'] == [{'type': 'text', 'text': REPLY + ':' + PROMPT}]
                    assert decoded['stop_reason'] == 'end_turn' and decoded['usage'] == {'input_tokens': 6, 'output_tokens': 4} and decoded['model'] == MODEL
                    assert headers['x-amzn-bedrock-input-token-count'] == '6' and headers['x-amzn-bedrock-output-token-count'] == '4'
                    assert capabilities(endpoint, token)['models'] == [ABSENT, MODEL]
            check('official boto3 Converse and InvokeModel (Anthropic Messages, foundation-model ARN) before and after restart')

            with simulator(directory, flags) as (endpoint, token):
                client = runtime_client(endpoint, token)
                before = len(ollama.requests)
                image = [{'role': 'user', 'content': [{'image': {'format': 'png', 'source': {'bytes': b'synthetic'}}}]}]
                assert error(lambda: client.converse(modelId=MODEL, messages=image)) == (400, 'ValidationException')
                assert error(lambda: converse(client, toolConfig={'tools': [{'toolSpec': {'name': 'synthetic', 'inputSchema': {'json': {}}}}]})) == (400, 'ValidationException')
                assert error(lambda: converse(client, inferenceConfig={'stopSequences': ['x']})) == (400, 'ValidationException')
                assert error(lambda: client.invoke_model(modelId=MODEL, body=json.dumps({'prompt': PROMPT}))) == (400, 'ValidationException')
                assert error(lambda: converse(client, model='meta.llama3-8b-instruct-v1:0')) == (404, 'ResourceNotFoundException')
                assert len(ollama.requests) == before, 'refused input reached the backend'
                assert error(lambda: converse(client, model=ABSENT)) == (404, 'ResourceNotFoundException')
                assert error(lambda: client.converse_stream(modelId=MODEL, messages=[{'role': 'user', 'content': [{'text': PROMPT}]}])) == (501, 'UnknownOperationException')
                assert error(lambda: client.invoke_model_with_response_stream(modelId=MODEL, body=b'{}')) == (501, 'UnknownOperationException')
                assert error(lambda: client.count_tokens(modelId=MODEL, input={'converse': {'messages': [{'role': 'user', 'content': [{'text': 'x'}]}]}})) == (501, 'UnknownOperationException')
                assert error(lambda: client.list_async_invokes()) == (501, 'UnknownOperationException')
                check('unsupported content, tools, stop sequences and model families refused; unmapped/uninstalled models 404; streaming 501')

                control = control_client(endpoint, token)
                created = control.create_guardrail(name='synthetic-guardrail', blockedInputMessaging='blocked', blockedOutputsMessaging='blocked',
                                                   wordPolicyConfig={'wordsConfig': [{'text': 'synthetic'}]})
                assert [item['id'] for item in control.list_guardrails()['guardrails']] == [created['guardrailId']]
                check('Bedrock control-plane catalog still serves /guardrails beside the runtime')

                participant = 'bedrock-learner'
                statement = [{'Effect': 'Allow', 'Action': 'bedrock:InvokeModel', 'Resource': 'arn:aws:bedrock:us-east-1::foundation-model/' + MODEL}]
                wire(endpoint, token, '/_simulator/v1/iam/principals/' + participant + '/policy', {'Version': '2012-10-17', 'Statement': statement}, method='PUT')
                learner = runtime_client(endpoint, wire(endpoint, token, '/_simulator/v1/access', {'Principal': participant})['AccessToken'])
                assert converse(learner)['stopReason'] == 'end_turn'
                before = len(ollama.requests)
                assert error(lambda: converse(learner, model=ABSENT)) == (403, 'AccessDeniedException')
                assert len(ollama.requests) == before, 'denied request reached the backend'
                check('participant token: allowed model succeeds, other model denied before the backend')

            ollama.close()
            with simulator(directory, flags) as (endpoint, token):
                assert error(lambda: converse(runtime_client(endpoint, token))) == (503, 'ServiceUnavailableException')
            check('stopped Ollama returns ServiceUnavailableException without fallback')

            for entry in Path(directory).glob('state.db*'):
                data = entry.read_bytes()
                assert PROMPT.encode() not in data and REPLY.encode() not in data, entry.name
            check('no prompt or reply in SQLite files; none in process output')
    finally:
        with contextlib.suppress(Exception):
            ollama.close()
    REPORTS.mkdir(parents=True, exist_ok=True)
    (REPORTS / 'bedrock-runtime.json').write_text(json.dumps(report, indent=1) + '\n')
    print('\n'.join(report['checks']))


if __name__ == '__main__':
    main()
