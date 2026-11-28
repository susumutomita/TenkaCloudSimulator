"""Frozen operation routes shared by the Logs and Secrets Manager reference fixtures."""

# main d166f569772de7f45d98e31e9971eeb33b903763 supplied 66 operations.
# The only subsequent addition in this inventory is S3MultipartUpload on POST /{bucket}/{key}.
EXPECTED_ROUTES = {
    '/': {'get': 'ListBuckets', 'post': 'awsRegionalOperation'},
    '/{bucket}': {
        'put': 'CreateBucket',
        'get': 'ListObjectsV2OrGetBucketLocation',
        'head': 'HeadBucket',
        'delete': 'DeleteBucket',
    },
    '/{bucket}/{key}': {
        'put': 'PutObject',
        'get': 'GetObject',
        'head': 'HeadObject',
        'delete': 'DeleteObject',
        'post': 'S3MultipartUpload',
    },
    '/_simulator/v1/capabilities': {'get': 'Capabilities'},
    '/_simulator/v1/iam/principals': {'get': 'ListPrincipals'},
    '/_simulator/v1/iam/principals/{principal}/policy': {
        'get': 'GetIdentityPolicy',
        'put': 'PutIdentityPolicy',
        'delete': 'DeleteIdentityPolicy',
    },
    '/2015-03-31/functions': {'get': 'ListFunctions', 'post': 'CreateFunction'},
    '/2015-03-31/functions/{FunctionName}/configuration': {'get': 'GetFunctionConfiguration'},
    '/2015-03-31/functions/{FunctionName}/code': {'put': 'UpdateFunctionCode'},
    '/2015-03-31/functions/{FunctionName}/invocations': {'post': 'Invoke'},
    '/2015-03-31/functions/{FunctionName}': {'delete': 'DeleteFunction'},
    '/_simulator/providers/aws/': {'post': 'IAMRoleQueryOperation'},
    '/restapis': {'post': 'CreateRestApi', 'get': 'GetRestApis'},
    '/restapis/{RestApiId}/resources': {'get': 'GetResources'},
    '/restapis/{RestApiId}': {'get': 'GetRestApi', 'delete': 'DeleteRestApi'},
    '/restapis/{RestApiId}/resources/{ResourceId}': {
        'post': 'CreateResource',
        'get': 'GetResource',
        'delete': 'DeleteResource',
    },
    '/restapis/{RestApiId}/resources/{ResourceId}/methods/{HttpMethod}': {
        'put': 'PutMethod',
        'get': 'GetMethod',
    },
    '/restapis/{RestApiId}/resources/{ResourceId}/methods/{HttpMethod}/integration': {'put': 'PutIntegration'},
    '/restapis/{RestApiId}/deployments': {'post': 'CreateDeployment'},
    '/restapis/{RestApiId}/deployments/{DeploymentId}': {'get': 'GetDeployment'},
    '/restapis/{RestApiId}/stages': {'post': 'CreateStage'},
    '/restapis/{RestApiId}/stages/{StageName}': {'get': 'GetStage', 'delete': 'DeleteStage'},
    '/_simulator/providers/aws/_execute-api/{Region}/{RestApiId}/{StageName}/{ProxyPath}': {
        'get': 'InvokeLocalRESTProxyGet',
        'post': 'InvokeLocalRESTProxyPost',
    },
    '/2015-03-31/functions/{FunctionName}/policy': {'post': 'AddPermission', 'get': 'GetPolicy'},
    '/2015-03-31/functions/{FunctionName}/policy/{StatementId}': {'delete': 'RemovePermission'},
    '/_simulator/providers/gcp/storage/v1/b': {'get': 'GCPListBuckets', 'post': 'GCPCreateBucket'},
    '/_simulator/providers/gcp/storage/v1/b/{BucketName}': {
        'get': 'GCPGetBucket',
        'delete': 'GCPDeleteBucket',
    },
    '/_simulator/providers/gcp/storage/v1/b/{BucketName}/o': {'get': 'GCPListObjects'},
    '/_simulator/providers/gcp/storage/v1/b/{BucketName}/o/{ObjectName}': {
        'get': 'GCPGetObject',
        'delete': 'GCPDeleteObject',
    },
    '/_simulator/providers/gcp/upload/storage/v1/b/{BucketName}/o': {'post': 'GCPUploadObject'},
    '/_simulator/providers/azure/{StorageAccount}': {'get': 'AzureListContainers'},
    '/_simulator/providers/azure/{StorageAccount}/{ContainerName}': {
        'put': 'AzureCreateContainer',
        'head': 'AzureGetContainerProperties',
        'get': 'AzureListBlobs',
        'delete': 'AzureDeleteContainer',
    },
    '/_simulator/providers/azure/{StorageAccount}/{ContainerName}/{BlobName}': {
        'put': 'AzurePutBlob',
        'get': 'AzureGetBlob',
        'head': 'AzureGetBlobProperties',
        'delete': 'AzureDeleteBlob',
    },
    '/_simulator/providers/gcp/download/storage/v1/b/{BucketName}/o/{ObjectName}': {
        'get': 'GCPDownloadObject',
    },
    '/_simulator/providers/sakura/cloud/zone/{zone}/api/cloud/1.1/disk': {
        'get': 'SakuraListDisks',
        'post': 'SakuraCreateDisk',
    },
    '/_simulator/providers/sakura/cloud/zone/{zone}/api/cloud/1.1/disk/{diskId}': {
        'get': 'SakuraGetDisk',
        'put': 'SakuraUpdateDisk',
        'delete': 'SakuraDeleteDisk',
    },
}

def verify_historical_routes(spec):
    paths = spec['paths']
    ids = [field['operationId'] for item in paths.values() for field in item.values()
           if isinstance(field, dict) and 'operationId' in field]
    assert len(ids) == len(set(ids)) == 67, 'Expected exactly 67 unique native operation IDs'
    assert paths.keys() == EXPECTED_ROUTES.keys(), 'The 36 historical native paths changed'
    for path, expected in EXPECTED_ROUTES.items():
        operations = {key: value for key, value in paths[path].items()
                      if key not in {'parameters', 'summary', 'description', 'servers', '$ref'}
                      and not key.startswith('x-')}
        assert operations.keys() == expected.keys(), f'Native HTTP methods changed at {path}'
        for method, operation_id in expected.items():
            operation = operations[method]
            assert isinstance(operation, dict) and operation.get('operationId') == operation_id, (
                f'Expected {operation_id} at {method.upper()} {path}')
