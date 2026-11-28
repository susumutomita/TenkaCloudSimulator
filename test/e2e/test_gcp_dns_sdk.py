"""Verify the pinned discovery client's local request paths without opening a socket."""
from types import SimpleNamespace
import unittest
from unittest.mock import Mock
from urllib.parse import urlsplit

from googleapiclient.discovery import build

from gcp_dns_sdk import checked_uri, project_and_endpoint


class CloudDNSClientPaths(unittest.TestCase):
    def test_discovery_client_adds_dns_v1_once(self):
        team = SimpleNamespace(port=4566, endpoint='http://127.0.0.1:4566')
        capabilities = {'cloudStorage': {'gcp': {
            'project': 'test-project', 'endpoint': '/_simulator/providers/gcp',
        }}}
        project, endpoint = project_and_endpoint(team, capabilities)
        http = Mock(spec=['request'])
        service = build('dns', 'v1', static_discovery=True, http=http,
                        client_options={'api_endpoint': endpoint})

        zones = service.managedZones()
        changes = service.changes()
        records = service.resourceRecordSets()
        collection = '/_simulator/providers/gcp/dns/v1/projects/test-project/managedZones'
        zone = collection + '/test-zone'
        for request, expected in (
            (zones.list(project=project), collection),
            (zones.create(project=project, body={'name': 'test-zone'}), collection),
            (changes.create(project=project, managedZone='test-zone', body={}), zone + '/changes'),
            (changes.get(project=project, managedZone='test-zone', changeId='1'), zone + '/changes/1'),
            (records.get(project=project, managedZone='test-zone', name='www.example.test.', type='A'),
             zone + '/rrsets/www.example.test./A'),
            (zones.delete(project=project, managedZone='test-zone'), zone),
        ):
            with self.subTest(operation=request.methodId):
                self.assertEqual(checked_uri(request.uri, team.port, collection), expected)
                self.assertEqual(urlsplit(request.uri).query, '' if request.method == 'DELETE' else 'alt=json')
        http.request.assert_not_called()


if __name__ == '__main__':
    unittest.main()
