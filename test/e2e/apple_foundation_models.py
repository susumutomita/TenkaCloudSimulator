"""Optional Apple helper boundary acceptance; synthetic content only.

Default: process double on Apple silicon macOS (no model call).
--helper /absolute/path: actual operator-built on-device helper and model.
No model download, settings changes, prompt/response reports or Claude Code.
"""
import argparse
import json
import platform
from pathlib import Path
import subprocess
import tempfile

from bedrock_runtime import (MODEL, PROMPT, REPLY, capabilities, error,
                             refused, runtime_client, simulator)


def fixture(path, failure=None):
    reply = {'version': 1, 'result': {'Text': REPLY, 'Stop': 'end', 'InputTokens': 9, 'OutputTokens': 3}}
    if failure:
        reply = {'version': 1, 'error': failure}
    path.write_text("#!/bin/sh\nif [ \"$1\" = --check ]; then\nprintf '%s' '{\"version\":1}'\nelse\nprintf '%s' '" + json.dumps(reply) + "'\nfi\n")
    path.chmod(0o700)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--helper', type=Path)
    args = parser.parse_args()
    if platform.system() != 'Darwin' or platform.machine() != 'arm64':
        refused(['--bedrock-runtime-backend', 'apple', '--bedrock-apple-helper', '/missing-native-helper',
                 '--bedrock-model', MODEL + '=system'], 'requires an Apple silicon Mac')
        print('Apple platform refusal passed; native generation unrun on this platform')
        return
    with tempfile.TemporaryDirectory(prefix='tcsim-apple-') as directory:
        helper = args.helper.resolve() if args.helper else Path(directory) / 'synthetic-helper'
        if not args.helper:
            fixture(helper)
        else:
            # Read readiness only; no response is written to the report or disk.
            checked = subprocess.run([str(helper), '--check'], capture_output=True, timeout=15, env={})
            ready = json.loads(checked.stdout)
            assert checked.returncode == 0 and ready == {'version': 1}, 'actual Apple model unavailable'
        texts = []
        with simulator(directory, ['--bedrock-runtime-backend', 'apple', '--bedrock-apple-helper', str(helper),
                                   '--bedrock-model', MODEL + '=system', '--bedrock-runtime-timeout', '60s'], private_texts=texts) as (endpoint, token):
            client = runtime_client(endpoint, token)
            listed = capabilities(endpoint, token)
            assert listed['backend'] == 'apple' and listed['enabled'] is True
            request = {'anthropic_version': 'bedrock-2023-05-31', 'max_tokens': 64,
                       'system': 'Reply in a few words.', 'temperature': 0.2, 'top_k': 10,
                       'messages': [{'role': 'user', 'content': 'Say hello briefly. ' + PROMPT}]}
            invoked = client.invoke_model(modelId=MODEL, body=json.dumps(request))
            value = json.loads(invoked['body'].read())
            texts.extend(block['text'] for block in value['content'])
            assert texts and all(texts), 'expected generated text'
            assert value['stop_reason'] == 'end_turn' and value['usage']['output_tokens'] > 0
            conversed = client.converse(modelId=MODEL, system=[{'text': 'Reply in a few words.'}],
                messages=[{'role': 'user', 'content': [{'text': 'Hello. ' + PROMPT}]},
                          {'role': 'assistant', 'content': [{'text': 'Hello, synthetic friend.'}]},
                          {'role': 'user', 'content': [{'text': 'Say goodbye briefly.'}]}],
                inferenceConfig={'maxTokens': 64, 'temperature': 0.2, 'topP': 0.8})
            texts.extend(block['text'] for block in conversed['output']['message']['content'])
            assert conversed['stopReason'] == 'end_turn' and conversed['usage']['outputTokens'] > 0
            request['top_p'] = 0.8
            assert error(lambda: client.invoke_model(modelId=MODEL, body=json.dumps(request))) == (424, 'ModelErrorException')
            del request['top_p']
            if args.helper:
                request['max_tokens'] = 1
                # The native API has no stop reason; at-cap replies are refused.
                assert error(lambda: client.invoke_model(modelId=MODEL, body=json.dumps(request))) == (424, 'ModelErrorException')
            else:
                fixture(helper, 'modelNotReady')
                assert error(lambda: client.invoke_model(modelId=MODEL, body=json.dumps(request))) == (503, 'ServiceUnavailableException')
                fixture(helper, 'synthetic-private-error')
                assert error(lambda: client.invoke_model(modelId=MODEL, body=json.dumps(request))) == (424, 'ModelErrorException')
        # Inspect simulator state after shutdown, without storing model text.
        for path in Path(directory).glob('state.db*'):
            stored = path.read_bytes()
            assert PROMPT.encode() not in stored
            assert all(text.encode() not in stored for text in texts if len(text) >= 8), 'response persisted'
    print('Apple ' + ('actual on-device model' if args.helper else 'synthetic process double') +
          ': InvokeModel, Converse/history, sampling, refusal and no-persistence passed; Claude Code untested')


if __name__ == '__main__':
    main()
