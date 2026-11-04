"""Narrow OpenAPI parameter-list integrity check (not a full schema validator).

OAS 3.1.0 Path Item/Operation parameters are unique by (name, in), but an
operation may legally override an inherited path parameter. Local references
are resolved before checking each list independently; no network is used.
https://spec.openapis.org/oas/v3.1.0.html#path-item-object
https://spec.openapis.org/oas/v3.1.0.html#operation-object
"""
from urllib.parse import unquote

METHODS = {'get', 'put', 'post', 'delete', 'options', 'head', 'patch', 'trace'}


def validate_parameter_lists(spec):
    """Raise ValueError for unresolved parameter refs or duplicate list entries."""
    def resolve(value, where):
        seen = set()
        while isinstance(value, dict) and '$ref' in value:
            ref = value['$ref']
            if not isinstance(ref, str) or not ref.startswith('#/'):
                raise ValueError(f'{where}: only local JSON pointer references supported: {ref!r}')
            if ref in seen:
                raise ValueError(f'{where}: cyclic reference {ref}')
            seen.add(ref)
            value = spec
            try:
                for part in unquote(ref[2:]).split('/'):
                    if '~' in part.replace('~1', '').replace('~0', ''):
                        raise KeyError(part)
                    part = part.replace('~1', '/').replace('~0', '~')
                    value = value[part]
            except (KeyError, TypeError):
                raise ValueError(f'{where}: unknown reference {ref}') from None
        if not isinstance(value, dict):
            raise ValueError(f'{where}: reference/entry must resolve to an object')
        return value

    def parameter(value, where):
        value = resolve(value, where)
        name, location = value.get('name'), value.get('in')
        if not isinstance(name, str) or not name or not isinstance(location, str) or location not in {'query', 'header', 'path', 'cookie'}:
            raise ValueError(f'{where}: parameter needs a name and valid in location')
        return name, location

    def check_list(owner, where):
        if 'parameters' not in owner:
            return
        entries = owner['parameters']
        if not isinstance(entries, list):
            raise ValueError(f'{where}: parameters must be an array')
        seen = set()
        for index, entry in enumerate(entries):
            key = parameter(entry, f'{where}/parameters/{index}')
            if key in seen:
                raise ValueError(f'{where}: duplicate parameter {key!r}')
            seen.add(key)

    visited = set()

    def callback(value, where):
        value = resolve(value, where)
        for expression, item in value.items():
            if expression not in {'summary', 'description'} and not expression.startswith('x-'):
                path_item(item, f'{where}/{expression}')

    def path_item(value, where):
        value = resolve(value, where)
        # Shared path items/callbacks can form a graph; each object is checked once.
        if id(value) in visited:
            return
        visited.add(id(value))
        check_list(value, where)
        for method in METHODS & value.keys():
            operation = value[method]
            if not isinstance(operation, dict):
                raise ValueError(f'{where}/{method}: operation must be an object')
            check_list(operation, f'{where}/{method}')
            for name, item in operation.get('callbacks', {}).items():
                callback(item, f'{where}/{method}/callbacks/{name}')

    for section in ('paths', 'webhooks'):
        for name, item in spec.get(section, {}).items():
            if not name.startswith('x-'):
                path_item(item, f'{section}/{name}')
    components = spec.get('components', {})
    for name, value in components.get('parameters', {}).items():
        parameter(value, f'components/parameters/{name}')
    for name, value in components.get('pathItems', {}).items():
        path_item(value, f'components/pathItems/{name}')
    for name, value in components.get('callbacks', {}).items():
        callback(value, f'components/callbacks/{name}')
