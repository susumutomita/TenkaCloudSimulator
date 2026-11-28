"""Compare the applied Service Quotas value with two teams' real B2BI profile counts."""
import tempfile

from botocore.exceptions import ClientError
from aws_catalog import Team


QUOTA = {'ServiceCode': 'b2bi', 'QuotaCode': 'L-05F6A9EF'}
ACTION = 'servicequotas:GetServiceQuota'


def quota_arn(region):
    return f'arn:aws:servicequotas:{region}:000000000000:b2bi/L-05F6A9EF'


def refused(code, status, operation, **kwargs):
    try:
        operation(**kwargs)
    except ClientError as error:
        assert error.response['Error']['Code'] == code, error.response['Error']['Code']
        assert error.response['ResponseMetadata']['HTTPStatusCode'] == status
    else:
        raise AssertionError(f'{operation.__name__} unexpectedly succeeded')


def read(team, region, token=None):
    client = team.client('service-quotas', token=token, region=region)
    quota = client.get_service_quota(**QUOTA)['Quota']
    assert quota == {**QUOTA, 'QuotaArn': quota_arn(region), 'QuotaName': 'Profiles', 'Value': 5.0,
                     'Adjustable': True, 'GlobalQuota': False, 'QuotaAppliedAtLevel': 'ACCOUNT'}
    return quota['Value']


def create_profile(client, name):
    return client.create_profile(name=name, businessName='Local fixture', phone='+1 555 0100',
                                 logging='DISABLED')['profileId']


def profiles(client):
    return {p['profileId'] for p in client.list_profiles()['profiles']}


def participant(team, name, statements):
    team.control('PUT', f'/_simulator/v1/iam/principals/{name}/policy',
                 {'Version': '2012-10-17', 'Statement': statements})
    return team.control('POST', '/_simulator/v1/access', {'Principal': name})['AccessToken']


def exercise(a, b):
    east, west = 'us-east-1', 'us-west-2'
    allow = {'Effect': 'Allow', 'Action': ACTION, 'Resource': quota_arn(east)}
    reader = participant(a, 'quota-east-reader', [allow])
    read(a, east, reader)
    refused('AccessDeniedException', 400, a.client('service-quotas', reader, west).get_service_quota, **QUOTA)
    east_reader = a.client('service-quotas', reader, east)
    refused('AccessDeniedException', 400, east_reader.get_service_quota, ServiceCode='b2bi', QuotaCode='L-00000000')
    deny = participant(a, 'quota-denied', [{'Effect': 'Allow', 'Action': ACTION, 'Resource': '*'},
                                          {**allow, 'Effect': 'Deny'}])
    refused('AccessDeniedException', 400, a.client('service-quotas', deny, east).get_service_quota, **QUOTA)
    admin = a.client('service-quotas', region=east)
    refused('NoSuchResourceException', 400, admin.get_service_quota, ServiceCode='kms', QuotaCode=QUOTA['QuotaCode'])
    refused('IllegalArgumentException', 400, admin.get_service_quota, **QUOTA, ContextId='arbitrary-context')
    refused('NoSuchResourceException', 400, a.client('service-quotas', region='ap-northeast-1').get_service_quota, **QUOTA)
    refused('UnknownOperationException', 501, admin.request_service_quota_increase, **QUOTA, DesiredValue=10)

    first, other_region, peer = a.client('b2bi', region=east), a.client('b2bi', region=west), b.client('b2bi', region=east)
    assert not profiles(first) and not profiles(other_region) and not profiles(peer)
    east_ids = {create_profile(first, f'profile-{n}') for n in range(int(read(a, east)))}
    assert len(east_ids) == 5
    refused('ServiceQuotaExceededException', 400, first.create_profile, name='overflow',
            businessName='Local fixture', phone='+1 555 0100', logging='DISABLED')
    assert profiles(first) == east_ids and read(a, east) == 5
    west_id = create_profile(other_region, 'independent-west')
    peer_id = create_profile(peer, 'independent-team')
    assert read(a, west) == 5 and read(b, east) == 5
    removed = east_ids.pop()
    first.delete_profile(profileId=removed)
    replacement = create_profile(first, 'replacement')
    assert replacement != removed
    east_ids.add(replacement)
    a.stop()
    a.start()
    first, other_region = a.client('b2bi', region=east), a.client('b2bi', region=west)
    assert profiles(first) == east_ids and profiles(other_region) == {west_id} and profiles(peer) == {peer_id}
    assert read(a, east) == read(a, west) == read(b, east) == 5
    for client, owned in [(first, east_ids), (other_region, {west_id}), (peer, {peer_id})]:
        for profile_id in owned:
            client.delete_profile(profileId=profile_id)
        assert not profiles(client)
    assert read(a, east) == 5


def main():
    with tempfile.TemporaryDirectory(prefix='quota-team-a-') as first, tempfile.TemporaryDirectory(prefix='quota-team-b-') as peer:
        teams = Team(first), Team(peer)
        try:
            for team in teams:
                team.start()
            exercise(*teams)
        finally:
            for team in teams:
                if team.process is not None:
                    team.stop()
    print('Service Quotas and B2BI native SDK: five profiles, sixth refused, replacement, IAM, regions, two teams, restart and cleanup passed')


if __name__ == '__main__':
    main()
