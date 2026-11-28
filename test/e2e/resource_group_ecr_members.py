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


def permission(team, name, statements, region='us-east-1'):
    team.control('PUT', f'/_simulator/v1/iam/principals/{name}/policy',
                 {'Version': '2012-10-17', 'Statement': statements})
    token = team.control('POST', '/_simulator/v1/access', {'Principal': name})['AccessToken']
    return team.client('resource-groups', token=token, region=region)


def check_service_actions(owner, peer):
    admin = owner.client('resource-groups')
    west = owner.client('resource-groups', region='us-west-2')
    other = peer.client('resource-groups')
    create = 'resource-groups:CreateGroup'
    listing = 'resource-groups:ListGroups'
    prefix = 'arn:aws:resource-groups:us-east-1:000000000000:group/'

    def grant(actions, target):
        return {'Effect': 'Allow', 'Action': actions, 'Resource': target}

    def arns(page):
        current = [item['GroupArn'] for item in page['GroupIdentifiers']]
        assert [item['GroupArn'] for item in page['Groups']] == current
        assert all('ApplicationTag' not in item for item in page['Groups'])
        return current

    limited = permission(owner, 'group-kind-only', [grant([create, listing], prefix + '*')])
    named = permission(owner, 'group-name-only', [grant([create, listing], prefix + 'scope-one')])
    explicit = permission(owner, 'group-service-denied', [grant([create, listing], '*'),
                          {'Effect': 'Deny', 'Action': [create, listing], 'Resource': '*'}])
    creator = permission(owner, 'group-create-only', [grant(create, '*')])
    list_only = [grant(listing, '*')]
    reader = permission(owner, 'group-list-only', list_only)
    assert arns(reader.list_groups()) == []
    for client in (limited, named, explicit):
        refused('ForbiddenException', 403, client.create_group, Name='scope-one', Tags={'Team': 'refused'})
        refused('ForbiddenException', 403, client.list_groups)
    assert arns(admin.list_groups()) == []
    tags = {'Team': 'kept', 'awsApplication': 'ordinary-user-value'}
    created = creator.create_group(Name='scope-one', Tags=tags)
    queried = creator.create_group(Name='scope-two', ResourceQuery=tag_query('blue'), Tags=tags)
    assert created['Tags'] == tags and queried['Tags'] == tags
    assert 'ApplicationTag' not in created['Group'] and 'ApplicationTag' not in queried['Group']
    first, second = created['Group']['GroupArn'], queried['Group']['GroupArn']
    remote = west.create_group(Name='scope-west')['Group']['GroupArn']
    sibling = other.create_group(Name='scope-peer')['Group']['GroupArn']
    assert first == prefix + 'scope-one' and second == prefix + 'scope-two'
    first_page = reader.list_groups(MaxResults=1)
    assert arns(first_page) == [first]
    token = first_page['NextToken']
    next_page = reader.list_groups(MaxResults=1, NextToken=token)
    assert arns(next_page) == [second] and 'NextToken' not in next_page
    for client in (limited, named, explicit):
        refused('ForbiddenException', 403, client.create_group, Name='scope-one', Tags={'Team': 'refused'})
        refused('ForbiddenException', 403, client.list_groups, NextToken=token)
    assert admin.get_tags(Arn=first)['Tags'] == tags
    admin.untag(Arn=second, Keys=['awsApplication'])
    admin.tag(Arn=second, Tags={'Team': 'changed'})
    assert admin.get_tags(Arn=second)['Tags'] == {'Team': 'changed'}
    assert 'ApplicationTag' not in admin.get_group(Group=second)['Group']
    west_reader = permission(owner, 'group-list-only', list_only, 'us-west-2')
    peer_reader = permission(peer, 'group-list-only', list_only)
    assert arns(west_reader.list_groups()) == [remote] and arns(peer_reader.list_groups()) == [sibling]
    owner.stop()
    owner.start()
    admin = owner.client('resource-groups')
    west = owner.client('resource-groups', region='us-west-2')
    token = owner.control('POST', '/_simulator/v1/access', {'Principal': 'group-list-only'})['AccessToken']
    reader = owner.client('resource-groups', token=token)
    assert 'ApplicationTag' not in admin.get_group(Group=first)['Group']
    assert admin.get_tags(Arn=first)['Tags'] == tags
    assert arns(reader.list_groups()) == [first, second] and arns(peer_reader.list_groups()) == [sibling]
    for client, names in ((admin, (first, second)), (west, (remote,)), (other, (sibling,))):
        for name in names:
            client.delete_group(Group=name)
        assert arns(client.list_groups()) == []


def exercise(a, b):
    check_service_actions(a, b)
    team, peer = a.client('resource-groups'), b.client('resource-groups')
    west = a.client('resource-groups', region='us-west-2')
    ecr, peer_ecr = a.client('ecr'), b.client('ecr')
    west_ecr = a.client('ecr', region='us-west-2')
    reference = a.control('GET', '/_simulator/docs/catalog/resource-groups.json')
    for path, operation in [('get-group-query', 'GetGroupQuery'), ('update-group-query', 'UpdateGroupQuery'),
                            ('list-group-resources', 'ListGroupResources')]:
        post = reference['paths']['/' + path]['post']
        assert post['summary'] == operation and 'Group' in post['requestBody']['content']['application/json']['schema']['properties']
        assert post['responses']['403']['description'] == 'ForbiddenException.'
    listing = reference['paths']['/list-group-resources']['post']
    output = listing['responses']['200']['content']['application/json']['schema']['properties']
    assert {'Resources', 'ResourceIdentifiers', 'NextToken'} <= set(output)
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
    try:
        b.client('resource-groups', token=a.token).list_group_resources(Group=peer_group)
    except ClientError as error:
        assert error.response['ResponseMetadata']['HTTPStatusCode'] == 401
    else:
        raise AssertionError('another team token was accepted')
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
    peer.delete_group(Group=peer_group)
    assert peer_ecr.describe_repositories(repositoryNames=['peer'])['repositories'][0]['repositoryArn'] == peer_arn
    assert peer_ecr.list_tags_for_resource(resourceArn=peer_arn)['tags'] == [{'Key': 'Team', 'Value': 'blue'}]
    refused('NotFoundException', 404, peer.get_group, Group=peer_group)
    for client, names in [(ecr, ('first', 'opposite')), (west_ecr, ('west',)), (peer_ecr, ('peer',))]:
        for repo in names:
            client.delete_repository(repositoryName=repo)
        assert not client.describe_repositories()['repositories']
    for client, group_arn in [(team, group), (team, other_group), (west, west_group)]:
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
