"""Exact additive batch-reference projections, independent of runtime or browser.

Each delta is pinned before removal. Projection preserves all unknown fields so
historical whole-document and component hashes can still detect unrelated edits.
"""
import hashlib
import json

JSON11 = 'application/x-amz-json-1.1'
SCHEMAS = ('SSMDeleteParametersInput', 'SSMDeleteParametersOutput')
BASE_TEXT = 'GetParametersByPath, DescribeParameters, DeleteParameter. CloudFormation:'
NEW_TEXT = 'GetParametersByPath, DescribeParameters, DeleteParameter, DeleteParameters. DeleteParameters requires 1–10 names and ssm:DeleteParameters on every distinct target, including missing names. The simulator validates and authorizes the complete batch before one atomic transaction. Duplicate names appear once in first-occurrence order within DeletedParameters or InvalidParameters. These ordering and atomicity guarantees are local bounds, not AWS equivalence. CloudFormation:'


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()


def project_ssm_batch(spec):
    """Validate the complete reviewed SSM delta before restoring its base view."""
    post = spec['paths']['/']['post']
    schemas = spec['components']['schemas']
    incoming = post['requestBody']['content'][JSON11]
    outgoing = post['responses']['200']['content'][JSON11]
    assert post['description'].count(NEW_TEXT) == 1, 'SSM batch description changed'
    delta = {
        'descriptionDelta': NEW_TEXT,
        'operationSchemas': post.pop('x-simulator-ssm-operation-schemas', None),
        'request': schemas.pop(SCHEMAS[0], None),
        'response': schemas.pop(SCHEMAS[1], None),
        'requestExample': incoming['examples'].pop('SSMDeleteParameters', None),
        'responseExample': outgoing['examples'].pop('SSMDeleteParameters', None),
    }
    assert digest(delta) == '1d3b03dcc28eb28833cdacb431001fc530deb440c0f9c030d871ec5a90bf4a33', 'SSM batch contract changed'
    for union, name in ((incoming['schema']['anyOf'], SCHEMAS[0]),
                        (outgoing['schema']['anyOf'], SCHEMAS[1])):
        ref = {'$ref': '#/components/schemas/' + name}
        assert union.count(ref) == 1 and union[-1] == ref, 'SSM batch union binding changed'
        union.pop()
    post['description'] = post['description'].replace(NEW_TEXT, BASE_TEXT)


def project_sqs_batch(spec):
    """Validate the exact additive batch contract before restoring the old view."""
    post = spec['paths']['/']['post']
    media = post['requestBody']['content']['application/x-amz-json-1.0']
    schemas = spec['components']['schemas']
    assert len(media['examples']) == 17
    delta = {
        'description': post['description'],
        'requestDescription': media['schema']['description'],
        'example': media['examples'].pop('DeleteMessageBatch', None),
        'request': schemas.pop('SQSDeleteMessageBatchRequest', None),
        'response': schemas.pop('SQSDeleteMessageBatchResult', None),
        'operationSchemas': post.pop('x-simulator-sqs-operation-schemas', None),
        'errorDescription': schemas['SQSAPIError']['properties']['__type']['description'],
    }
    assert digest(delta) == '229b00e1f09a8522bc1f3f2f7c740fd420ddd5751463d4c11a46f01d60e50f5d', 'SQS batch contract changed'
    for union, name in ((media['schema']['anyOf'], 'SQSDeleteMessageBatchRequest'),
                        (post['responses']['200']['content']['application/x-amz-json-1.0']['schema']['anyOf'],
                         'SQSDeleteMessageBatchResult')):
        ref = {'$ref': '#/components/schemas/' + name}
        assert union.count(ref) == 1 and union[-1] == ref, 'SQS batch union binding changed'
        union.pop()
    suffix = '\n\nSQS DeleteMessageBatch: 1–10 distinct entry IDs, IAM sqs:DeleteMessage on the queue ARN. HTTP200 always includes Successful and Failed arrays; inspect both even on success. Invalid/stale/expired/peer receipts are per-entry SenderFault failures. Local partitions retain input order and repeated handles with different IDs succeed only on the first occurrence, unlike an AWS ordering guarantee. A required SQL, cancellation, commit or acknowledgement failure returns sanitized500 and rolls back every deletion and scoped expiry cleanup, without a success list. No batch console action is added.'
    assert post['description'].endswith(suffix)
    post['description'] = post['description'][:-len(suffix)]
    for added, original in (
            ('ReceiveMessage, DeleteMessage, DeleteMessageBatch, ChangeMessageVisibility',
             'ReceiveMessage, DeleteMessage, ChangeMessageVisibility'),
            ('FIFO, Query protocol, SendMessageBatch, ChangeMessageVisibilityBatch and dead-letter redrive',
             'FIFO, Query protocol, batch operations and dead-letter redrive')):
        assert post['description'].count(added) == 1
        post['description'] = post['description'].replace(added, original)
    assert media['schema']['description'].count('ten SQS') == 1
    media['schema']['description'] = media['schema']['description'].replace('ten SQS', 'nine SQS')
    suffix = ' DeleteMessageBatch also returns EmptyBatchRequest, TooManyEntriesInBatchRequest, BatchEntryIdsNotDistinct and InvalidBatchEntryId.'
    assert delta['errorDescription'].endswith(suffix)
    schemas['SQSAPIError']['properties']['__type']['description'] = delta['errorDescription'][:-len(suffix)]



def project_batches(spec):
    """Restore the common base after validating both independent additions."""
    project_ssm_batch(spec)
    project_sqs_batch(spec)
