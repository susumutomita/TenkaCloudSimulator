"""Start a real isolated simulator using its ordinary CLI. Preserve the inherited environment."""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import socket
import subprocess
from tempfile import TemporaryDirectory
import time
from urllib.parse import urlsplit
from urllib.request import ProxyHandler, HTTPRedirectHandler, Request, build_opener


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        raise AssertionError('local receiver returned unexpected redirect')


_opener = build_opener(ProxyHandler({}), NoRedirect())


def local_open(request,timeout=5):
    url = urlsplit(request.full_url)
    assert url.scheme == 'http' and url.hostname == '127.0.0.1' and url.port and url.username is None and url.password is None
    return _opener.open(request,timeout=timeout)


@contextmanager
def running(*,db,authenticated=True):
    assert authenticated
    binary = Path(os.environ.get('SIMULATOR_BINARY','dist/tenkacloud-simulator')).resolve(strict=True)
    with TemporaryDirectory(prefix='tenka-witness-authority-') as directory:
        token_file = Path(directory)/'token'
        with socket.socket() as s:
            s.bind(('127.0.0.1',0));port=s.getsockname()[1]
        endpoint=f'http://127.0.0.1:{port}'
        process=subprocess.Popen([str(binary),'--listen',f'127.0.0.1:{port}','--state',str(Path(db).resolve()),'--token-file',str(token_file)],cwd=directory,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        try:
            deadline=time.monotonic()+25
            while time.monotonic()<deadline:
                if process.poll() is not None: raise AssertionError('fixture CLI exited before readiness')
                if token_file.is_file():
                    token=token_file.read_text().strip()
                    if token:
                        try:
                            with local_open(Request(endpoint+'/_simulator/health',headers={'X-Simulator-Token':token}),timeout=1) as resp:
                                if resp.status==200:break
                        except OSError:pass
                time.sleep(.05)
            else:raise AssertionError('fixture CLI health timeout')
            yield endpoint,token
        finally:
            if process.poll() is None:process.terminate()
            try: status=process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill();process.wait();raise
            if status!=0:raise AssertionError('fixture CLI did not terminate cleanly')


def control(endpoint,token,path,body):
    assert path.startswith('/_simulator/v1/')
    method='POST' if path=='/_simulator/v1/access' else 'PUT'
    r=Request(endpoint+path,method=method,data=json.dumps(body).encode(),headers={'Content-Type':'application/json','X-Simulator-Token':token})
    with local_open(r) as resp:return json.load(resp)
