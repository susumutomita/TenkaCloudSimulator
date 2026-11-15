#!/usr/bin/env python3
"""Loopback-only build helper for the offline guest image builder VM.

GET /cdn/<path>   -> streams https://cdn.amazonlinux.com/<path> fetched through
                     the host's configured HTTPS proxy with TLS verification on.
GET /local/<name> -> serves a regular file from the local directory.

Every upstream fetch is appended to a provenance log with its SHA-256. RPM
signatures are still verified inside the guest (gpgcheck=1) with the key that
ships in the official image; this relay is transport only.
"""
import hashlib
import http.server
import json
import os
import ssl
import sys
import time
import urllib.request

UPSTREAM = 'https://cdn.amazonlinux.com/'
local_dir, log_path, port = sys.argv[1], sys.argv[2], int(sys.argv[3])
context = ssl.create_default_context(cafile=os.environ.get('SSL_CERT_FILE') or '/root/.ccr/ca-bundle.crt')
opener = urllib.request.build_opener(urllib.request.ProxyHandler(), urllib.request.HTTPSHandler(context=context))


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.0'

    def log_message(self, *args):
        pass

    def record(self, entry):
        entry['ts'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
        with open(log_path, 'a') as log:
            log.write(json.dumps(entry, sort_keys=True) + '\n')

    def do_GET(self):
        path = self.path.split('?', 1)[0]
        if '..' in path or '\\' in path:
            self.send_error(400)
            return
        if path.startswith('/local/'):
            name = path[len('/local/'):]
            target = os.path.join(local_dir, name)
            if '/' in name or not os.path.isfile(target) or os.path.islink(target):
                self.send_error(404)
                return
            data = open(target, 'rb').read()
            self.send_response(200)
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            self.record({'local': name, 'sha256': hashlib.sha256(data).hexdigest(), 'bytes': len(data)})
            return
        if not path.startswith('/cdn/al2023/'):
            self.send_error(404)
            return
        url = UPSTREAM + path[len('/cdn/'):]
        try:
            with opener.open(url, timeout=120) as response:
                data = response.read()
        except urllib.error.HTTPError as error:
            self.record({'url': url, 'status': error.code})
            self.send_error(error.code)
            return
        except Exception as error:  # transport failure is reported, never masked
            self.record({'url': url, 'error': type(error).__name__})
            self.send_error(502)
            return
        self.send_response(200)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)
        self.record({'url': url, 'status': 200, 'sha256': hashlib.sha256(data).hexdigest(), 'bytes': len(data)})


http.server.ThreadingHTTPServer(('127.0.0.1', port), Handler).serve_forever()
