"""Resolve a named Resource Groups query against real tagged ECR repositories."""
import json
import tempfile

from botocore.exceptions import ClientError
from aws_catalog import Team


RESOURCE_TYPE = 'AWS::ECR::Repository'
ACTIONS = ('resource-groups:ListGroupResources', 'cloudformation:DescribeStacks',
           'cloudformation:ListStackResources', 'tag:GetResources')


def tag_query(value):
    return {'Type': 'TAG_FILTERS_1_0', 'Query': json.dumps(
        {'ResourceTypeFilters': [RESOURCE_TYPE], 'TagFilters': [{'Key': 'Team', 'Values': [value]}]})}


def refused(code, status, operation, **args):
    try:
        operation(**args)
    except ClientError as error:
        assert error.response['Error']['Code'] == code, error.response['Error']['Code']
        assert error.response['ResponseMetadata']['HTTPStatusCode'] == status
    else:
        raise AssertionError(f'{operation.__name__} unexpectedly succeeded')


def list_members(client, group, **args):
    result = client.list_group_resources(Group=group, **args)
    identifiers = result['ResourceIdentifiers']
    assert result['Resources'] == [{'Identifier': identifier} for identifier in identifiers]
    assert all(set(i) == {'ResourceType', 'ResourceArn'} and i['ResourceType'] == RESOURCE_TYPE for i in identifiers)
    return [i['ResourceArn'] for i in identifiers], result.get('NextToken')


def create_repo(client, name, value):
    return client.create_repository(repositoryName=name, tags=[{'Key': 'Team', 'Value': value}])['repository']['repositoryArn']


def permission(team, name, statements):
    team.control('PUT', f'/_simulator/v1/iam/principals/{name}/policy',
                 {'Version': '2012-10-17', 'Statement': statements})
    token = team.control('POST', '/_simulator/v1/access', {'Principal': name})['AccessToken']
    return team.client('resource-groups', token=token)


def exercise(a, b):
    team, peer = a.client('resource-groups'), b.client('resource-groups')
    west = a.client('resource-groups', region='us-west-2')
    ecr, peer_ecr = a.client('ecr'), b.client('ecr')
    west_ecr = a.client('ecr', region='us-west-2')
    blue, green = tag_query('blue'), tag_query('green')
    name = 'release-repositories'
    group = team.create_group(Name=name, ResourceQuery=blue)['Group']['GroupArn']
    peer_group = peer.create_group(Name=name, ResourceQuery=blue)['Group']['GroupArn']
    west_group = west.create_group(Name=name, ResourceQuery=blue)['Group']['GroupArn']
    assert peer_group == group and west_group != group
    assert list_members(team, name) == ([], None)
    first, second = create_repo(ecr, 'first', 'blue'), create_repo(ecr, 'second', 'blue')
    opposite = create_repo(ecr, 'opposite', 'green')
    peer_arn, west_arn = create_repo(peer_ecr, 'peer', 'blue'), create_repo(west_ecr, 'west', 'blue')
    assert list_members(team, group) == ([first, second], None)
    assert list_members(peer, peer_group) == ([peer_arn], None) and list_members(west, west_group) == ([west_arn], None)
    page, token = list_members(team, group, MaxResults=1)
    assert page == [first] and token is not None
    assert list_members(team, group, NextToken=token) == ([second], None)
    refused('BadRequestException', 400, west.list_group_resources, Group=west_group, NextToken=token)
    other_group = team.create_group(Name='other-group', ResourceQuery=blue)['Group']['GroupArn']
    refused('BadRequestException', 400, team.list_group_resources, Group=other_group, NextToken=token)

    allows = [{'Effect': 'Allow', 'Action': action, 'Resource': group if action == ACTIONS[0] else '*'} for action in ACTIONS]
    reader = permission(a, 'named-group-reader', allows)
    assert list_members(reader, name) == ([first, second], None)
    refused('ForbiddenException', 403, reader.list_group_resources, Group=other_group)
    for index in range(len(ACTIONS)):
        partial = permission(a, f'group-no-permission-{index}', allows[:index] + allows[index+1:])
        refused('ForbiddenException', 403, partial.list_group_resources, Group=group, NextToken=token)
    denied = permission(a, 'group-explicit-deny', [*allows, {'Effect': 'Deny', 'Action': 'tag:GetResources', 'Resource': '*'}])
    refused('ForbiddenException', 403, denied.list_group_resources, Group=group)
    ecr.untag_resource(resourceArn=first, tagKeys=['Team'])
    ecr.tag_resource(resourceArn=first, tags=[{'Key': 'team', 'Value': 'blue'}])
    assert list_members(team, group) == ([second], None)
    ecr.tag_resource(resourceArn=first, tags=[{'Key': 'Team', 'Value': 'Blue'}])
    assert list_members(team, group) == ([second], None)
    ecr.tag_resource(resourceArn=first, tags=[{'Key': 'Team', 'Value': 'blue'}])
    assert list_members(team, group) == ([first, second], None)
    ecr.delete_repository(repositoryName='second')
    assert list_members(team, group) == ([first], None)
    assert team.update_group_query(Group=group, ResourceQuery=green)['GroupQuery']['ResourceQuery'] == green
    refused('BadRequestException', 400, team.list_group_resources, Group=group, NextToken=token)
    assert list_members(team, group) == ([opposite], None)
    a.stop()
    a.start()
    team, west, ecr, west_ecr = a.client('resource-groups'), a.client('resource-groups', region='us-west-2'), a.client('ecr'), a.client('ecr', region='us-west-2')
    assert team.get_group_query(Group=name)['GroupQuery'] == {'GroupName': name, 'ResourceQuery': green}
    assert list_members(team, group) == ([opposite], None)
    assert list_members(peer, peer_group) == ([peer_arn], None) and list_members(west, west_group) == ([west_arn], None)
    for client, names in [(ecr, ('first', 'opposite')), (west_ecr, ('west',)), (peer_ecr, ('peer',))]:
        for repo in names:
            client.delete_repository(repositoryName=repo)
        assert not client.describe_repositories()['repositories']
    for client, group_arn in [(team, group), (team, other_group), (west, west_group), (peer, peer_group)]:
        assert list_members(client, group_arn) == ([], None)
        client.delete_group(Group=group_arn)
        refused('NotFoundException', 404, client.get_group, Group=group_arn)


def main():
    with tempfile.TemporaryDirectory(prefix='group-ecr-team-') as first, tempfile.TemporaryDirectory(prefix='group-ecr-peer-') as peer:
        teams = Team(first), Team(peer)
        try:
            for team in teams:
                team.start()
            exercise(*teams)
        finally:
            for team in teams:
                if team.process is not None:
                    team.stop()
    print('ECR tag-based named groups: real matching ARNs, paging, tag changes, deletion, IAM, region/team isolation, restart and cleanup passed')


if __name__ == '__main__':
    main()
