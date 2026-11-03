class ClientError(Exception):
    def __init__(self, error_response, operation_name):
        self.response = error_response
        self.operation_name = operation_name
        error = error_response.get('Error', {})
        super().__init__('%s: %s' % (error.get('Code', 'Unknown'), error.get('Message', 'Local operation failed')))
