"""Replay the served read-only Query preset against a fresh authenticated local simulator.

No browser, external endpoint, real cloud, guest or cloud resource mutation.
"""
import http.client
import json
import os
from pathlib import Path
from urllib.parse import urlencode
import xml.etree.ElementTree as ET

from ec2_launch_preflight_native import EC2, Local, REGIONS, run_default


def check(binary):
    with run_default(binary) as (port, token):
        local = Local(port)
        cookie = local.session(token)
        conn = http.client.HTTPConnection('127.0.0.1', port, timeout=8)
        try:
            conn.request('GET', '/_simulator/docs/openapi.json', headers={'Cookie': cookie})
            reply = conn.getresponse()
            assert reply.status == 200, reply.status
            limit = 2 * 1024 * 1024
            raw = reply.read(limit + 1)
            assert len(raw) <= limit
            post = json.loads(raw)['paths']['/']['post']
        finally:
            conn.close()

        mime = 'application/x-www-form-urlencoded'
        form = post['requestBody']['content'][mime]
        value = form['examples']['DescribeInstances']['value']
        assert value == {'Action': 'DescribeInstances', 'Version': '2016-11-15'}
        services, = (h['schema']['enum'] for h in post['parameters'] if h.get('name') == 'X-Simulator-Service')
        assert 'ec2' in services
        for region in REGIONS:
            headers = {'Content-Type': mime, 'X-Simulator-Service': 'ec2', 'X-Simulator-Region': region}
            _, wire = local.call('POST', '/', urlencode(value), Cookie=cookie, **headers)
            response = ET.fromstring(wire)
            assert response.tag == EC2 + 'DescribeInstancesResponse'
            rows = response.find(EC2 + 'reservationSet')
            assert rows is not None and len(rows) == 0, 'fresh region unexpectedly contains instance data'
            print(f'{region}: served DescribeInstances preset -> HTTP 200, empty EC2 reservationSet')
        local.call('POST', '/', urlencode(value), status=401, **headers)
        print('No session or token: HTTP 401')


if __name__ == '__main__':
    check(str(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)))
