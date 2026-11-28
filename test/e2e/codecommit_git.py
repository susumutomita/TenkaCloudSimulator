"""Read and write the official CodeCommit SDK's real local Git history.

Run on a neutral runner with the pinned boto3 and Git test oracle. The simulator
process has an empty executable search path and owns its team database.
"""
from contextlib import ExitStack
import hashlib
from http.client import HTTPConnection
import json
import os
from pathlib import Path
import subprocess
import tempfile
from urllib.parse import urlsplit

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from sts_session_lifecycle import LocalSimulator


def control(team, method, path, data):
    conn = HTTPConnection('127.0.0.1', team.port, timeout=5)
    try:
        conn.request(method, path, json.dumps(data), {'Content-Type': 'application/json', 'X-Simulator-Token': team.token})
        response = conn.getresponse()
        assert 200 <= response.status < 300, (method, path, response.status)
        return json.loads(response.read())
    finally:
        conn.close()


def git_object(kind, data):
    return subprocess.run(['git', 'hash-object', '-t', kind, '--stdin'], input=data,
                          stdout=subprocess.PIPE, check=True).stdout.decode().strip()


def git_tree(entries):
    encoded = b''.join(mode + b' ' + name + b'\0' + bytes.fromhex(oid)
                       for mode, name, oid in sorted(entries, key=lambda e: e[1] + (b'/' if e[0] == b'40000' else b'')))
    return git_object('tree', encoded)


def verify_commit(client, repo, record, tree, parents, message):
    commit = client.get_commit(repositoryName=repo, commitId=record['commitId'])['commit']
    assert commit['treeId'] == tree == record['treeId'] and commit['parents'] == parents
    assert commit['message'] == message and commit['author']['name'] == 'Fixture writer'
    assert commit['author']['email'] == 'writer@example.invalid' and commit['author'] == commit['committer']
    ident = '{name} <{email}> {date}'.format(**commit['author'])
    raw = 'tree ' + tree + '\n' + ''.join('parent ' + parent + '\n' for parent in parents)
    raw += f'author {ident}\ncommitter {ident}\n\n{message}'
    assert git_object('commit', raw.encode()) == commit['commitId'] == record['commitId']


def refusal(code, call, **fields):
    try:
        call(**fields)
    except ClientError as error:
        assert error.response['Error']['Code'] == code, (call.__name__, error.response['Error']['Code'])
        return
    raise AssertionError(f'{call.__name__} unexpectedly succeeded')


def exercise(binary):
    targets, counts = set(), {'send': 0, 'response': 0}
    with tempfile.TemporaryDirectory(prefix='codecommit-git-') as directory, ExitStack() as stack:
        root = Path(directory)
        teams = [LocalSimulator(root / name, binary) for name in ('owner', 'peer')]
        for team in teams:
            stack.callback(team.close)
            team.start()
        owner, peer = teams

        def client(team, token, region='us-east-1'):
            sdk = boto3.client('codecommit', endpoint_url=team.endpoint, region_name=region,
                               aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy',
                               config=Config(retries={'max_attempts': 0}, proxies={}))
            stack.callback(sdk.close)
            sdk.meta.events.register('before-sign.codecommit', lambda request, **kw: request.headers.add_header('X-Simulator-Token', token))

            def sent(request, **kwargs):
                url = urlsplit(request.url)
                assert (url.scheme, url.netloc, url.path, url.query) == ('http', f'127.0.0.1:{team.port}', '/', '')
                assert request.method == 'POST' and b'/' + region.encode() + b'/codecommit/aws4_request' in request.headers['Authorization']
                assert request.headers['Content-Type'] == b'application/x-amz-json-1.1'
                prefix, name = request.headers['X-Amz-Target'].decode().split('.')
                assert prefix == 'CodeCommit_20150413'
                targets.add(name)
                counts['send'] += 1

            def received(http_response, **kwargs):
                assert not 300 <= http_response.status_code < 400
                counts['response'] += 1

            sdk.meta.events.register('before-send.codecommit', sent)
            sdk.meta.events.register('after-call.codecommit', received)
            return sdk

        a, b = client(owner, owner.token), client(peer, peer.token)
        other_region = client(owner, owner.token, 'us-west-2')
        repo = 'git-practice'
        for sdk in (a, b, other_region):
            sdk.create_repository(repositoryName=repo)
            assert sdk.list_branches(repositoryName=repo)['branches'] == []
        native = dict(repositoryName=repo)
        author = dict(name='Fixture writer', email='writer@example.invalid')
        data = b'example\0\xff\n'
        first = a.put_file(**native, **author, branchName='main', filePath='dir/item.bin',
                           fileContent=data, commitMessage='Initial\n')
        blob = git_object('blob', data)
        assert first['blobId'] == blob
        directory_tree = git_tree([(b'100644', b'item.bin', blob)])
        verify_commit(a, repo, first, git_tree([(b'40000', b'dir', directory_tree)]), [], 'Initial\n')
        assert a.get_repository(**native)['repositoryMetadata']['defaultBranch'] == 'main'
        for team_sdk in (b, other_region):
            refusal('CommitIdDoesNotExistException', team_sdk.get_commit, **native, commitId=first['commitId'])
        fork = 'release/v1'
        a.create_branch(**native, branchName=fork, commitId=first['commitId'])
        extra = b'read me\n'
        following = a.put_file(**native, **author, branchName='main', parentCommitId=first['commitId'],
                               fileMode='EXECUTABLE', filePath='dir.txt', fileContent=extra, commitMessage='Update\n')
        extra_id = git_object('blob', extra)
        assert following['blobId'] == extra_id
        tree = git_tree([(b'40000', b'dir', directory_tree), (b'100755', b'dir.txt', extra_id)])
        verify_commit(a, repo, following, tree, [first['commitId']], 'Update\n')
        assert a.get_file(**native, filePath='/dir/item.bin')['fileContent'] == data
        refusal('ParentCommitIdOutdatedException', a.put_file, **native, filePath='dir.txt', fileContent=b'bad',
                branchName='main', parentCommitId=first['commitId'])
        changed = a.put_file(**native, **author, branchName=fork, parentCommitId=first['commitId'],
                             filePath='dir/item.bin', fileContent=b'', commitMessage='Empty')
        assert a.get_file(**native, filePath='dir/item.bin', commitSpecifier='refs/heads/' + fork)['fileContent'] == b''
        assert a.get_file(**native, filePath='dir/item.bin', commitSpecifier=first['commitId'])['fileContent'] == data
        assert a.get_branch(**native, branchName='main')['branch']['commitId'] == following['commitId']
        policy = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': 'codecommit:GetFile',
                  'Resource': f'arn:aws:codecommit:us-east-1:000000000000:{repo}'}]}
        control(owner, 'PUT', '/_simulator/v1/iam/principals/git-reader/policy', policy)
        reader = client(owner, control(owner, 'POST', '/_simulator/v1/access', {'Principal': 'git-reader'})['AccessToken'])
        assert reader.get_file(**native, filePath='dir.txt')['fileContent'] == extra
        refusal('AccessDeniedException', reader.put_file, **native, filePath='dir.txt', fileContent=b'denied',
                branchName='main', parentCommitId=following['commitId'])
        for team in teams:
            team.close()
            team.start()
        assert a.get_branch(**native, branchName=fork)['branch']['commitId'] == changed['commitId']
        assert a.get_file(**native, filePath='dir/item.bin', commitSpecifier=first['commitId'])['fileContent'] == data
        assert a.get_file(**native, filePath='dir.txt')['fileMode'] == 'EXECUTABLE'
        assert b.list_branches(**native)['branches'] == []
        a.delete_repository(**native)
        a.create_repository(**native)
        assert a.list_branches(**native)['branches'] == []
        refusal('CommitIdDoesNotExistException', a.get_commit, **native, commitId=first['commitId'])
        for sdk in (a, b, other_region):
            sdk.delete_repository(**native)
    assert not root.exists() and counts['send'] == counts['response']
    with binary.open('rb') as stream:
        binary_sha = hashlib.file_digest(stream, 'sha256').hexdigest()
    return {'binarySha256': binary_sha, 'operations': sorted(targets),
            'sends': counts['send'], 'responses': counts['response'], 'gitHashesChecked': True,
            'reopenedTeams': 2, 'ownedDirectoriesRemoved': True}


if __name__ == '__main__':
    print(json.dumps(exercise(Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve(strict=True))))
