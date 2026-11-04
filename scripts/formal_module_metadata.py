"""Recognize a small require-annotation/grouping-only go.mod change.

This is intentionally NOT a Go parser or a dependency validator. Unsupported
syntax fails closed. Inputs must be immutable Git blobs checked by the caller.
"""
import re

_PART = r'[A-Za-z0-9][A-Za-z0-9._~+-]*'
_PATH = rf'{_PART}(?:/{_PART})*'
_NUMBER = r'(?:0|[1-9][0-9]*)'
# Only stable three-component releases are recognized. Pseudo/prerelease and
# quoted/escaped syntax require explicit policy review, even if valid in Go.
_VERSION = rf'v{_NUMBER}\.{_NUMBER}\.{_NUMBER}'
_REQUIRE = re.compile(rf'({_PATH})[ \t]+({_VERSION})(?:[ \t]+// indirect)?')


def _requirements(data):
    if (not isinstance(data, bytes) or len(data) > 65536 or not data.endswith(b'\n')
            or any(c > 127 or c < 32 and c not in (9, 10) for c in data)):
        return None
    header, requirements, block = [], {}, False
    started, block_entries = False, 0
    module, go, toolchain = False, False, False
    for raw in data.decode('ascii').splitlines(keepends=True):
        line = raw[:-1]
        if not line.strip(' \t'):
            if not started:
                header.append(raw)
            continue
        if block:
            if line == ')':
                if not block_entries:
                    return None
                block = False
                continue
            spec = line.strip(' \t')
        elif line == 'require (':
            if not module or not go:
                return None
            started, block, block_entries = True, True, 0
            continue
        elif line.startswith('require '):
            if not module or not go:
                return None
            started = True
            spec = line[len('require '):]
        else:
            # All non-require directives are a fixed byte-identical prefix.
            # Unknown directives/comments, even unchanged ones, are unsupported.
            if started:
                return None
            if not module and re.fullmatch(rf'module {_PATH}', line):
                module = True
            elif module and not go and re.fullmatch(rf'go [1-9][0-9]*\.{_NUMBER}(?:\.{_NUMBER})?', line):
                go = True
            elif module and go and not toolchain and re.fullmatch(rf'toolchain (?:default|go[1-9][0-9]*\.{_NUMBER}(?:\.{_NUMBER})?)', line):
                toolchain = True
            else:
                return None
            header.append(raw)
            continue
        match = _REQUIRE.fullmatch(spec)
        if not match:
            return None
        path, version = match.groups()
        if path in requirements or any(part in ('.', '..') for part in path.split('/')):
            return None
        requirements[path] = version
        if block:
            block_entries += 1
    if block or not requirements or not module or not go:
        return None
    return ''.join(header).encode('ascii'), requirements


def require_metadata_only(before, after, sum_before, sum_after):
    """True only for the recognized same-requirements, same-checksum subset."""
    left, right = _requirements(before), _requirements(after)
    return (left is not None and right is not None and left == right
            and isinstance(sum_before, bytes) and isinstance(sum_after, bytes)
            and sum_before == sum_after)
