#!/usr/bin/env python3
"""Pinned community SDK against two owned standalone processes; no cloud access."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile

from sakura_cli import BASE, CLI, create, frame


def run(binary, client, report):
    owned = []
    with tempfile.TemporaryDirectory(prefix='sakura-sdk-') as directory:
        root = Path(directory)
        try:
            for label in ('a', 'b'):
                cli = CLI(binary, root / label, 'sdk-team-' + label)
                owned.append(cli)
                assert create(cli, 'sentinel') == '1'
            a, b = owned
            assert a.process.pid != b.process.pid and a.token != b.token
            for cli, peer in ((a, b), (b, a)):
                before = frame(peer.db)
                participant = cli.call('POST', '/_simulator/v1/access',
                                       {'Principal': 'learner'})['AccessToken']
                completed = subprocess.run(
                    [client], input=json.dumps({'Origin': cli.origin, 'Token': participant}),
                    text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=45,
                    env={'PATH': '/usr/bin:/bin', 'CI': '1'})
                assert completed.returncode == 0, 'SDK client failed; output redacted'
                assert completed.stdout == 'Sakura SDK: 9 real Disk calls passed\n'
                assert not completed.stderr
                assert frame(peer.db) == before, 'peer SQLite changed'
                disks = cli.call('GET', BASE)['Disks']
                assert len(disks) == 1 and disks[0]['ID'] == '1'
                assert disks[0]['Name'] == 'sentinel'
                cli.call('DELETE', BASE + '/1')
                assert cli.call('GET', BASE)['Disks'] == []
        finally:
            for cli in reversed(owned):
                cli.close()
    result = {
        'success': True,
        'binarySHA256': hashlib.sha256(Path(binary).read_bytes()).hexdigest(),
        'sdkCommit': 'd8e98d8762e049e3ffd40bc70667cf9ad20029c0',
        'sdkCallsPerProcess': 9,
        'processes': 2,
        'checks': ['real SDK serializer and decoder', 'five Disk operations',
                   'JSON pagination', 'neutral Icon', '501 and 404 SDK errors',
                   'description clear', 'participant credentials',
                   'peer SQLite preservation', 'native cleanup'],
        'unverified': ['stock SDK authentication/endpoint/retry/waiter behavior',
                       'real cloud equivalence', 'VM or block I/O'],
    }
    Path(report).write_text(json.dumps(result, indent=2) + '\n')
    print('Sakura SDK acceptance passed: two standalone processes, 18 SDK calls')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--client', required=True)
    parser.add_argument('--report', required=True)
    args = parser.parse_args()
    run(str(Path(args.binary).resolve(strict=True)),
        str(Path(args.client).resolve(strict=True)), args.report)
