"""Validate the five KMS grant and deletion additions before checking the pre-grant API."""

GRANT_AND_DELETION_ACTIONS = (
    'CreateGrant', 'ListGrants', 'RevokeGrant', 'ScheduleKeyDeletion', 'CancelKeyDeletion',
)
REQUEST_SCHEMAS = (
    'KMSDescribeKeyInput', 'KMSCreateGrantInput', 'KMSListGrantsInput',
    'KMSRevokeGrantInput', 'KMSScheduleKeyDeletionInput',
)
RESPONSE_SCHEMAS = (
    'KMSCreateGrantResponse', 'KMSGrantPage', 'KMSScheduledDeletion', 'KMSCanceledDeletion',
)


def remove_union_additions(branches, preceding, names):
    """Recognize the complete ordered block immediately after the inherited KMS schema."""
    previous = {'$ref': '#/components/schemas/' + preceding}
    additions = [{'$ref': '#/components/schemas/' + name} for name in names]
    assert branches.count(previous) == 1, 'KMS reference union anchor changed'
    start = branches.index(previous) + 1
    end = start + len(additions)
    assert branches[start:end] == additions, 'KMS reference union order changed'
    assert all(branches.count(row) == 1 for row in additions), 'KMS reference union duplicated'
    del branches[start:end]


def project_before_kms_grants_and_deletion(spec, digest):
    """Operate on the caller's private historical projection, keeping unrelated fields intact."""
    post = spec['paths']['/']['post']
    media = 'application/x-amz-json-1.1'
    request = post['requestBody']['content'][media]
    response = post['responses']['200']['content'][media]
    operations = post['x-simulator-kms-operation-schemas']
    schemas = spec['components']['schemas']

    added_examples = {name: request['examples'].pop('KMS' + name, None) for name in GRANT_AND_DELETION_ACTIONS}
    assert digest(added_examples) == 'f727159eca848b23f67d3a5ae3b3bc6bebe0f5b17dc6616f6ddc762716fda906', 'KMS grant/deletion examples changed'
    added_operations = {name: operations.pop(name, None) for name in GRANT_AND_DELETION_ACTIONS}
    assert digest(added_operations) == '787000f27efe672a00d591cd8e6ac5af117f2410f1d64064fff2d4959bf4de79', 'KMS grant/deletion operations changed'
    added_schemas = {name: schemas.pop(name, None) for name in (*REQUEST_SCHEMAS, *RESPONSE_SCHEMAS, 'KMSGrantEntry')}
    assert digest(added_schemas) == '55eb9ef6e9bcb61383bf0b2365f99804c1be0f5f041fb1f63c01853139c0aa14', 'KMS grant/deletion schemas changed'

    remove_union_additions(request['schema']['anyOf'], 'KMSDeleteAliasInput', REQUEST_SCHEMAS)
    remove_union_additions(response['schema']['anyOf'], 'KMSAliasPage', RESPONSE_SCHEMAS)

    current_request = operations['DescribeKey']['request']
    assert current_request == {'$ref': '#/components/schemas/KMSDescribeKeyInput'}, 'KMS DescribeKey request changed'
    current_request['$ref'] = '#/components/schemas/KMSKeyInput'
    properties = schemas['KMSKeyMetadata']['properties']
    assert properties.pop('DeletionDate', None) == {
        'type': 'number', 'description': 'UTC deadline in seconds since the Unix epoch, present while PendingDeletion.',
    }, 'KMS deletion date changed'
    states = properties['KeyState']['enum']
    assert states == ['Enabled', 'Disabled', 'PendingDeletion'], 'KMS key states changed'
    states.pop()
