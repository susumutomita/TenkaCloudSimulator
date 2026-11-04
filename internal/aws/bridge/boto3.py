"""Bounded local SDK seam; no credentials, sockets or arbitrary endpoints."""
import base64
import io
from _simulator_bridge import call
from botocore.exceptions import ClientError

_OPERATIONS = {
    'ssm': {'get_parameter': 'GetParameter', 'get_parameters': 'GetParameters'},
    's3': {'get_object': 'GetObject', 'put_object': 'PutObject', 'delete_object': 'DeleteObject'},
}

class _Client:
    def __init__(self, service, region):
        self._service, self._region = service, region
    def __getattr__(self, name):
        operation = _OPERATIONS[self._service].get(name)
        if operation is None:
            raise NotImplementedError('This local SDK operation is unavailable')
        def invoke(**parameters):
            if operation == 'PutObject' and 'Body' in parameters:
                body = parameters['Body']
                if isinstance(body, str):
                    body = body.encode('utf-8')
                if not isinstance(body, bytes):
                    raise TypeError('Body must be bytes or a string')
                if len(body) > 32768:
                    raise ValueError('Local SDK object bodies support at most 32 KiB')
                parameters['Body'] = base64.b64encode(body).decode('ascii')
            request = {'service': self._service, 'operation': operation, 'parameters': parameters}
            if self._region is not None:
                request['region'] = self._region
            response = call(request)
            if 'error' in response:
                error = response['error']
                raise ClientError({'Error': {'Code': error['code'], 'Message': error['message']}, 'ResponseMetadata': {'HTTPStatusCode': error.get('status', 500)}}, operation)
            result = response['result']
            if operation == 'GetObject':
                result['Body'] = io.BytesIO(base64.b64decode(result['Body']))
            return result
        return invoke

def client(service_name, region_name=None, **kwargs):
    if kwargs or service_name not in _OPERATIONS:
        raise NotImplementedError('Only local SSM/S3 clients without endpoints or credentials are supported')
    return _Client(service_name, region_name)
