"""Real local Kinesis Video ingestion and playback using the published Java producer and boto3.

Prepare pinned Java dependencies before network isolation using --prepare-java DIR.
Then run --java-libs DIR with SIMULATOR_BINARY and SIMULATOR_BINARY_SHA256 from the build.
The runner needs a Java 17+ JDK, FFmpeg/FFprobe and the repository's pinned Python SDK.
"""
import argparse
from collections import Counter
from contextlib import ExitStack, closing
from decimal import Decimal
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.parse import urlsplit
import urllib.request

from azure_queue_cli import running_cli, verify_binary

HERE = Path(__file__).resolve().parent
JAVA = HERE / 'kinesis_video'
SOURCE_CLIP = HERE.parents[1] / 'internal/aws/kinesisvideo/testdata/two-frames.mkv'
REGION = 'us-east-1'
NAME = 'practice-video'
STAMPS = (1740000000125, 1740000012250)
ACKS = ['BUFFERING', 'RECEIVED', 'PERSISTED']


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


def libraries(directory, download=False):
    directory.mkdir(parents=True, exist_ok=True)
    result = []
    for line in (JAVA / 'maven.sha256').read_text().splitlines():
        if line.startswith('#'):
            continue
        digest, path = line.split()
        assert re.fullmatch('[0-9a-f]{64}', digest)
        assert re.fullmatch(r'[a-zA-Z0-9._/-]+\.jar', path) and '..' not in path and not path.startswith('/')
        jar = directory / Path(path).name
        if download and not jar.exists():
            url = 'https://repo.maven.apache.org/maven2/' + path
            with urllib.request.build_opener(NoRedirect()).open(url, timeout=30) as response:
                data = response.read(16 * 1024 * 1024 + 1)
            assert len(data) <= 16 * 1024 * 1024 and hashlib.sha256(data).hexdigest() == digest
            with tempfile.NamedTemporaryFile(dir=directory, delete=False) as tmp:
                tmp.write(data)
            os.replace(tmp.name, jar)
        if hashlib.sha256(jar.read_bytes()).hexdigest() != digest:
            raise ValueError('Java dependency checksum mismatch: ' + jar.name)
        assert jar not in result
        result.append(jar)
    return os.pathsep.join(str(jar.resolve()) for jar in result)


def producer(classpath, team, name, token, clip, stamp, mode='producer'):
    args = ['java', '-XX:ActiveProcessorCount=1', '-Daws.java.v1.disableDeprecationAnnouncement=true',
            '-cp', classpath, 'KinesisVideoProducer', mode, team.endpoint, name, str(clip), str(stamp)]
    run = subprocess.run(args, input=token + '\n', capture_output=True, text=True, timeout=30)
    if run.returncode:
        raise RuntimeError('Java PutMedia process failed')
    results = [json.loads(line.removeprefix('KINESIS_FIXTURE ')) for line in run.stdout.splitlines()
               if line.startswith('KINESIS_FIXTURE ')]
    assert len(results) == 1
    return results[0]


def upload(classpath, team, name, token, clip, stamp, *, retained=True, mode='producer'):
    response = producer(classpath, team, name, token, clip, stamp, mode)
    assert response['status'] == 200
    events = response['events']
    assert [row['event'] for row in events] == (ACKS if retained else ACKS[:2])
    assert all(row['timecode'] == 0 for row in events)
    number = events[-1]['number']
    assert all(row['number'] == number for row in events)
    if retained:
        assert isinstance(number, str) and number.isdecimal() and int(number) > 0
    else:
        assert number is None
    return number


def participant(team, role, arn, operations):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def submit(method, path, data):
        req = urllib.request.Request(team.endpoint + path, method=method, data=json.dumps(data).encode(),
                                    headers={'Content-Type': 'application/json', 'X-Simulator-Token': team.token})
        with opener.open(req, timeout=4) as response:
            return json.loads(response.read() or b'{}')

    submit('PUT', '/_simulator/v1/iam/principals/' + role + '/policy',
           {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Resource': arn,
             'Action': ['kinesisvideo:' + op for op in operations]}]})
    return submit('POST', '/_simulator/v1/access', {'Principal': role})['AccessToken']


def sdk(stack, session, counts, team, token, service='kinesisvideo', *, endpoint=None):
    from botocore.config import Config
    endpoint = endpoint or team.endpoint
    assert endpoint == team.endpoint and urlsplit(endpoint).hostname == '127.0.0.1'
    client = session.client(service, endpoint_url=endpoint, region_name=REGION,
                            aws_access_key_id='TCSIMCATALOG', aws_secret_access_key='dummy-local-only',
                            config=Config(proxies={}, retries={'total_max_attempts': 1}, connect_timeout=3, read_timeout=8))
    stack.callback(client.close)
    client.meta.events.register('before-sign', lambda request, **kw: request.headers.add_header('X-Simulator-Token', token))

    def before_send(request, **kwargs):
        parsed = urlsplit(request.url)
        assert parsed.scheme == 'http' and parsed.netloc == urlsplit(endpoint).netloc and not parsed.fragment
        assert request.method == 'POST' and parsed.path in ('/createStream', '/deleteStream', '/describeStream',
                                                            '/getDataEndpoint', '/getMedia')
        counts['sends'] += 1

    def called(**kwargs):
        counts['calls'] += 1

    def response(**kwargs):
        counts['responses'] += 1

    client.meta.events.register('before-call', called)
    client.meta.events.register('before-send', before_send)
    client.meta.events.register('after-call', response)
    return client


def refused(status, operation, **args):
    from botocore.exceptions import ClientError
    try:
        operation(**args)
    except ClientError as exc:
        assert exc.response['ResponseMetadata']['HTTPStatusCode'] == status
        return
    raise AssertionError('Expected SDK refusal')


def create(control, name, hours):
    from botocore.exceptions import ClientError
    refused(404, control.describe_stream, StreamName=name)
    try:
        arn = control.create_stream(StreamName=name, MediaType='video/h264', DataRetentionInHours=hours)['StreamARN']
        stream = control.describe_stream(StreamName=name)['StreamInfo']
        assert stream['Status'] == 'ACTIVE' and stream['StreamARN'] == arn and stream['DataRetentionInHours'] == hours
        return arn
    except BaseException:
        # A create reply can be lost. This exact name was absent in the privately owned process.
        try:
            saved = control.describe_stream(StreamName=name)['StreamInfo']
        except ClientError as exc:
            if exc.response['ResponseMetadata']['HTTPStatusCode'] != 404:
                raise
        else:
            assert saved['StreamName'] == name
            control.delete_stream(StreamARN=saved['StreamARN'])
        raise


def retired(control, name, arn):
    control.delete_stream(StreamARN=arn)
    refused(404, control.describe_stream, StreamName=name)


def data_clients(stack, session, totals, team, arn):
    writer = participant(team, 'practice-video-writer', arn, ['GetDataEndpoint', 'PutMedia'])
    read_token = participant(team, 'practice-video-reader', arn, ['GetDataEndpoint', 'GetMedia'])
    endpoint = {}
    for api, token in [('PUT_MEDIA', writer), ('GET_MEDIA', read_token)]:
        control = sdk(stack, session, totals, team, token)
        endpoint[api] = control.get_data_endpoint(StreamARN=arn, APIName=api)['DataEndpoint']
        assert endpoint[api] == team.endpoint
    media = sdk(stack, session, totals, team, read_token, 'kinesis-video-media', endpoint=endpoint['GET_MEDIA'])
    return writer, read_token, media


def read(reader, arn, cursor=None):
    start = ({'StartSelectorType': 'EARLIEST'} if cursor is None else
             {'StartSelectorType': 'CONTINUATION_TOKEN', 'ContinuationToken': cursor})
    response = reader.get_media(StreamARN=arn, StartSelector=start)
    assert response['ResponseMetadata']['HTTPStatusCode'] == 200
    with closing(response['Payload']) as body:
        data = body.read(512 * 1024 + 1)
        assert len(data) <= 512 * 1024 and body.read(1) == b''
        return data


def decode(raw):
    info = json.loads(subprocess.check_output(
        ['ffprobe', '-v', 'error', '-select_streams', 'v', '-show_format', '-show_streams', '-show_data', '-of', 'json', '-'],
        input=raw, timeout=10))
    video, = info['streams']
    assert (video['codec_name'], video['width'], video['height']) == ('h264', 32, 24)
    frame_md5 = subprocess.check_output(['ffmpeg', '-v', 'error', '-threads', '1', '-i', '-', '-an', '-f', 'framemd5', '-'],
                                        input=raw, timeout=10).decode()
    frames = [line.rsplit(',', 1)[-1].strip() for line in frame_md5.splitlines() if line and not line.startswith('#')]
    assert len(frames) == 2 and all(re.fullmatch('[a-f0-9]{32}', frame) for frame in frames)
    return (video['extradata'], frames), info['format']['tags']


def fragments(payload, sources, numbers, times):
    ebml = bytes.fromhex('1a45dfa3')
    prefix, *blocks = payload.split(ebml)
    assert prefix == b'' and len(blocks) == len(sources)
    tags = []
    for block, expected, number, millis in zip(blocks, sources, numbers, times, strict=True):
        observed, meta = decode(ebml + block)
        assert observed == expected
        assert meta['AWS_KINESISVIDEO_FRAGMENT_NUMBER'] == number
        assert Decimal(meta['AWS_KINESISVIDEO_PRODUCER_TIMESTAMP']) * 1000 == millis
        assert Decimal(meta['AWS_KINESISVIDEO_SERVER_TIMESTAMP']) > 0
        assert int(meta['AWS_KINESISVIDEO_MILLIS_BEHIND_NOW']) >= 0
        assert re.fullmatch('[a-zA-Z0-9_-]{32}', meta['AWS_KINESISVIDEO_CONTINUATION_TOKEN'])
        tags.append(meta)
    return tags


def exercise(binary, jars, directory):
    import boto3
    session = boto3.Session()
    classpath = libraries(jars)
    subprocess.run(['java', '-m', 'jdk.compiler/com.sun.tools.javac.Main', '-proc:none', '-cp', classpath,
                    '-d', str(directory), str(JAVA / 'KinesisVideoProducer.java')], check=True, timeout=30)
    classpath = str(directory) + os.pathsep + classpath
    alternate = directory / 'mirrored.mkv'
    subprocess.run(['ffmpeg', '-v', 'error', '-i', str(SOURCE_CLIP), '-map', '0:v:0', '-map_metadata', '-1',
                    '-an', '-vf', 'hflip', '-vsync', '0', '-c:v', 'libx264', '-threads', '1',
                    '-profile:v', 'baseline', '-preset', 'ultrafast', '-g', '30', '-bf', '0', '-flags:v', '+bitexact',
                    '-fflags', '+bitexact', '-f', 'matroska', str(alternate)], check=True, timeout=15)
    clips = (SOURCE_CLIP, alternate)
    sources = [decode(path.read_bytes())[0] for path in clips]
    assert sources[0][1] != sources[1][1]
    totals = Counter()
    with running_cli(binary, directory / 'peer') as peer, ExitStack() as peer_stack:
        peer_control = sdk(peer_stack, session, totals, peer, peer.token)
        other_arn = create(peer_control, NAME, 24)
        peer_stack.callback(retired, peer_control, NAME, other_arn)
        other_writer, _, peer_reader = data_clients(peer_stack, session, totals, peer, other_arn)
        peer_id = upload(classpath, peer, NAME, other_writer, alternate, STAMPS[1])
        peer_initial = read(peer_reader, other_arn)
        fragments(peer_initial, sources[1:], [peer_id], STAMPS[1:])
        with running_cli(binary, directory / 'owner') as owner, ExitStack() as stack:
            op = sdk(stack, session, totals, owner, owner.token)
            arn = create(op, NAME, 24)
            try:
                writer, read_token, reader = data_clients(stack, session, totals, owner, arn)
                for mode, token in [('netty', writer), ('producer', read_token), ('producer', other_writer)]:
                    assert producer(classpath, owner, NAME, token, clips[0], STAMPS[0], mode) == {'status': 401, 'events': []}
                write_only_reader = sdk(stack, session, totals, owner, writer, 'kinesis-video-media')
                refused(401, read, reader=write_only_reader, arn=arn)
                assert read(reader, arn) == b''
                numbers = [upload(classpath, owner, NAME, writer, clip, millis) for clip, millis in zip(clips, STAMPS, strict=True)]
                assert int(numbers[0]) < int(numbers[1])
                saved = read(reader, arn)
                metadata = fragments(saved, sources, numbers, STAMPS)
                cursor = metadata[0]['AWS_KINESISVIDEO_CONTINUATION_TOKEN']
                assert cursor != metadata[1]['AWS_KINESISVIDEO_CONTINUATION_TOKEN']
                assert fragments(read(reader, arn, cursor), sources[1:], numbers[1:], STAMPS[1:]) == metadata[1:]
                refused(400, read, reader=peer_reader, arn=other_arn, cursor=cursor)
                zero_name = NAME + '-zero'
                zero = create(op, zero_name, 0)
                try:
                    zero_writer = participant(owner, 'practice-video-zero', zero, ['PutMedia', 'GetMedia'])
                    zero_reader = sdk(stack, session, totals, owner, zero_writer, 'kinesis-video-media')
                    for mode in ('producer', 'producer-iso'):
                        upload(classpath, owner, zero_name, zero_writer, clips[0], STAMPS[0], retained=False, mode=mode)
                    assert read(zero_reader, zero) == b''
                finally:
                    retired(op, zero_name, zero)
            except BaseException:
                retired(op, NAME, arn)
                raise
        with running_cli(binary, directory / 'owner') as reopened, ExitStack() as stack:
            op = sdk(stack, session, totals, reopened, reopened.token)
            try:
                assert op.describe_stream(StreamName=NAME)['StreamInfo']['StreamARN'] == arn
                _, _, fresh_reader = data_clients(stack, session, totals, reopened, arn)
                persisted = read(fresh_reader, arn)
                assert persisted == saved and fragments(persisted, sources, numbers, STAMPS) == metadata
                assert read(peer_reader, other_arn) == peer_initial
            finally:
                retired(op, NAME, arn)
            replacement = create(op, NAME, 24)
            try:
                assert replacement != arn
                operator_reader = sdk(stack, session, totals, reopened, reopened.token, 'kinesis-video-media')
                refused(404, read, reader=operator_reader, arn=arn)
                assert read(operator_reader, replacement) == b''
                refused(400, read, reader=operator_reader, arn=replacement, cursor=cursor)
            finally:
                retired(op, NAME, replacement)
    assert totals['calls'] == totals['sends'] == totals['responses'] and totals['sends'] > 30
    return {'boto3Calls': totals['sends'], 'producer': '1.12.0', 'netty': '1.12.797', 'fragmentNumbers': numbers,
            'frameMD5': [item[1] for item in sources], 'inputSHA256': [hashlib.sha256(p.read_bytes()).hexdigest() for p in clips],
            'acknowledgments': ACKS, 'zeroRetentionAcknowledgments': ACKS[:2], 'nettyWithoutLocalHeader': 401,
            'readerAsWriter': 401, 'writerAsReader': 401, 'foreignParticipant': 401, 'cursorSelectedSecond': True,
            'sameNamePeerUnchanged': True, 'reopenedDecoded': True, 'cleanup': True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--prepare-java', type=Path, metavar='DIR')
    mode.add_argument('--java-libs', type=Path, metavar='DIR')
    args = parser.parse_args()
    if args.prepare_java:
        libraries(args.prepare_java, download=True)
        return
    binary = verify_binary(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator'),
                           os.environ.get('SIMULATOR_BINARY_SHA256'))
    with tempfile.TemporaryDirectory(prefix='kinesis-video-sdk-') as temp:
        print(json.dumps(exercise(binary, args.java_libs, Path(temp)), sort_keys=True))


if __name__ == '__main__':
    main()
