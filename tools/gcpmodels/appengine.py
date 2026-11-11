"""Extract the documentation-free App Engine Admin API v1 subset.

Development-only. The simulator binary never imports Python. The subset uses
the same reduction as extract.py: every official method keeps its HTTP binding
(so unreviewed methods are refused with UNIMPLEMENTED), and only the reviewed
methods keep parameters and the schemas they reach. The reviewed list below is
the native App Engine provider's contract, not catalog metadata.

Usage: python tools/gcpmodels/appengine.py
"""
import json
import sys
from pathlib import Path

import importlib.metadata

sys.path.insert(0, str(Path(__file__).resolve().parent))
from extract import PINNED, ROOT, extract  # noqa: E402

OUTPUT = ROOT / 'internal/gcp/appengine/model.json'
REVIEWED = [
    'apps.create', 'apps.get', 'apps.patch',
    'apps.locations.get', 'apps.locations.list',
    'apps.operations.get', 'apps.operations.list',
    'apps.services.get', 'apps.services.list', 'apps.services.patch', 'apps.services.delete',
    'apps.services.versions.create', 'apps.services.versions.get', 'apps.services.versions.list',
    'apps.services.versions.patch', 'apps.services.versions.delete',
    'apps.firewall.ingressRules.create', 'apps.firewall.ingressRules.get', 'apps.firewall.ingressRules.list',
    'apps.firewall.ingressRules.patch', 'apps.firewall.ingressRules.delete', 'apps.firewall.ingressRules.batchUpdate',
]


def main():
    version = importlib.metadata.version('google-api-python-client')
    if version != PINNED:
        raise SystemExit(f'google-api-python-client {PINNED} is required; found {version}')
    model = extract('appengine.v1', REVIEWED)
    OUTPUT.write_text(json.dumps(model, sort_keys=True, separators=(',', ':')) + '\n')
    print(f'Extracted {len(model["methods"])} reviewed of {len(model["routes"])} official App Engine methods into {OUTPUT.relative_to(ROOT)}')


if __name__ == '__main__':
    sys.exit(main())
