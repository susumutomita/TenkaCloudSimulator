"""Validate and remove only the reviewed metadata-only UpdateSecret delta."""
from batch_reference_projection import digest, JSON11

def project_secret_description(spec):
    # Pin every reviewed UpdateSecret addition before projecting it away. The
    # pre-existing service digests below remain unchanged and still detect drift.
    operation = spec['paths']['/']['post']
    request = operation['requestBody']['content'][JSON11]
    response = operation['responses']['200']['content'][JSON11]
    assert len(request['examples']) == 31
    assert digest(request['examples'].pop('UpdateSecret', None)) == 'db2972742f1e15d728c5410cf10ede226b05831c6a90e8b975c709f09aef04d3', 'UpdateSecret contract changed: UpdateSecret'
    assert digest(response['examples'].pop('UpdateSecret', None)) == '4fb8eaa8926e8e726ffcc6b6fff0fc8736e5a94f0f431f0475910301c8f4c451', 'UpdateSecret contract changed: UpdateSecret'
    assert digest(operation['x-simulator-secrets-operation-schemas'].pop('secretsmanager.UpdateSecret', None)) == '5ca666ab206f582e9e17bc57508904904defdfd4a88b57ca7d916552f429bab4', 'UpdateSecret contract changed: secretsmanager.UpdateSecret'
    assert digest(spec['components']['schemas'].pop('SecretsUpdateSecretInput', None)) == '1fe4604e0dfbc27e581a32d4572717a03b59b10d4b0cf56cb7ca231dc52ad854', 'UpdateSecret contract changed: SecretsUpdateSecretInput'
    assert digest(spec['components']['schemas'].pop('SecretsUpdateSecretOutput', None)) == '401d72f452a2457dda3bc1248eef42a37d4be07200aafbb02c0b8e2c97d7010b', 'UpdateSecret contract changed: SecretsUpdateSecretOutput'
    for media, name in ((request, 'SecretsUpdateSecretInput'),
                        (response, 'SecretsUpdateSecretOutput')):
        ref = {'$ref': '#/components/schemas/' + name}
        union = media['schema']['anyOf']
        assert union.count(ref) == 1 and union[-1] == ref, 'UpdateSecret union binding changed'
        union.pop()
    tag = next(tag for tag in spec['tags'] if tag['name'] == 'Secrets Manager')
    assert digest(tag) == '383cd72aef5d551ad25af6350422de0f8c360a4e73e390fd9b91412e84d1e78f', 'UpdateSecret service description changed'
    tag['description'] = tag['description'].replace('Nine templates', 'Eight templates', 1)
