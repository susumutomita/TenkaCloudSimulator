"""SES v2 domain identity proof using the stock SDK and two real local CLIs."""

import argparse
from contextlib import ExitStack
from hashlib import sha256
from importlib.metadata import version
from pathlib import Path
from tempfile import TemporaryDirectory
from urllib.parse import urlsplit

from sts_session_lifecycle import LocalSimulator, need


def clients(session, config, team, stack, counters):
    result = []
    for service, root in (('sesv2', '/v2/email/identities'), ('route53', '/2013-04-01/')):
        client = session.client(service, region_name='us-east-1', endpoint_url=team.endpoint,
                                aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='simulator-only',
                                config=config)
        stack.callback(client.close)

        def before_sign(request, **kwargs):
            request.headers['X-Simulator-Token'] = team.token

        def before_send(request, prefix=root, **kwargs):
            target = urlsplit(request.url)
            need(target.scheme == 'http' and target.netloc == f'127.0.0.1:{team.port}'
                 and target.path.startswith(prefix) and not target.fragment,
                 'signed SDK request escaped the local service')
            need(request.headers.get('Authorization', b'').startswith(b'AWS4-HMAC-SHA256 '),
                 'stock SDK did not sign')
            counters['sent'] += 1

        def observed(http_response, **kwargs):
            need(200 <= http_response.status_code < 600, 'missing HTTP response')
            counters['responses'] += 1

        # The service event names are normalized by botocore, including SESv2.
        client.meta.events.register('before-sign.*', before_sign)
        client.meta.events.register('before-send.*', before_send)
        client.meta.events.register('after-call.*', observed)
        result.append(client)
    return result


def verify(binary):
    import boto3
    from botocore.config import Config
    from botocore.exceptions import ClientError

    need(version('boto3') == '1.42.0' and version('botocore') == '1.41.6', 'wrong pinned SDK')
    session = boto3.Session()
    config = Config(retries={'total_max_attempts': 1}, connect_timeout=3, read_timeout=8,
                    proxies={}, request_checksum_calculation='when_required',
                    response_checksum_validation='when_required')
    counters = {'sent': 0, 'responses': 0}
    with TemporaryDirectory(prefix='sim-ses-domain-') as temporary, ExitStack() as stack:
        directory = Path(temporary)
        owner, peer = [LocalSimulator(directory / name, binary) for name in ('owner','peer')]
        for team in (owner, peer):
            stack.callback(team.close)
            team.start()
        ses, dns = clients(session, config, owner, stack, counters)
        peer_ses, peer_dns = clients(session, config, peer, stack, counters)
        domain = 'fixture.mail.test'
        def create_zone(client):
            return client.create_hosted_zone(Name=domain, CallerReference='identity-proof')['HostedZone']['Id']
        zone, peer_zone = create_zone(dns), create_zone(peer_dns)
        def create(client):
            saved = client.create_email_identity(EmailIdentity=domain)
            tokens = saved['DkimAttributes']['Tokens']
            need(saved['IdentityType'] == 'DOMAIN' and saved['VerifiedForSendingStatus'] is False
                 and saved['DkimAttributes']['Status'] == 'PENDING' and len(set(tokens)) == 3,
                 'new identity was not a pending three-token domain')
            return tokens
        tokens, peer_tokens = create(ses), create(peer_ses)
        need(set(tokens).isdisjoint(peer_tokens),'peer reused a challenge')
        def change(client, zone_id, token, action):
            record = {'Name': f'{token}._domainkey.{domain}.', 'Type': 'CNAME', 'TTL': 60,
                      'ResourceRecords': [{'Value': f'{token}.dkim.amazonses.com.'}]}
            response = client.change_resource_record_sets(
                HostedZoneId=zone_id, ChangeBatch={'Changes': [{'Action': action, 'ResourceRecordSet': record}]})
            need(response['ChangeInfo']['Status'] == 'INSYNC', 'DNS change not saved')
        def read(client, saved_tokens, state):
            current = client.get_email_identity(EmailIdentity=domain)
            need(current['DkimAttributes']['Tokens'] == saved_tokens and
                 current['DkimAttributes']['Status'] == current['VerificationStatus'] == state and
                 current['VerifiedForSendingStatus'] == (state == 'SUCCESS'), 'unexpected saved identity')
            listed = client.list_email_identities(PageSize=1)
            need([(r['IdentityName'],r['VerificationStatus']) for r in listed['EmailIdentities']] == [(domain,state)]
                 and 'NextToken' not in listed, 'list did not match the same native identity')
        for token in tokens[:2]:
            change(dns,zone,token,'CREATE')
        read(ses,tokens,'PENDING')
        change(dns,zone,tokens[2],'CREATE')
        read(ses,tokens,'SUCCESS')
        read(peer_ses,peer_tokens,'PENDING')
        owner.close()
        owner.start()
        read(ses,tokens,'SUCCESS')
        change(dns,zone,tokens[2],'DELETE')
        read(ses,tokens,'PENDING')
        for token in tokens[:2]:
            change(dns,zone,token,'DELETE')
        for sc,dc,z in ((ses,dns,zone),(peer_ses,peer_dns,peer_zone)):
            sc.delete_email_identity(EmailIdentity=domain)
            try:
                sc.get_email_identity(EmailIdentity=domain)
            except ClientError as error:
                need(error.response['Error']['Code'] == 'NotFoundException', 'wrong deleted identity result')
            else:
                raise AssertionError('deleted identity unexpectedly exists')
            need(sc.list_email_identities()['EmailIdentities'] == [], 'left a domain identity')
            dc.delete_hosted_zone(Id=z)
            need(dc.list_hosted_zones()['HostedZones'] == [], 'left a hosted zone')
    need(counters['sent'] == counters['responses'] and counters['sent'] >= 30, 'incomplete real SDK requests')
    print(f"SESv2/Route53 stock SDK passed {counters['sent']} signed requests and responses; two teams, restart, cleanup")


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--binary-sha256', required=True)
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    with binary.open('rb') as source:
        from hashlib import file_digest
        need(file_digest(source, sha256).hexdigest() == args.binary_sha256, 'wrong simulator executable')
    verify(binary)
