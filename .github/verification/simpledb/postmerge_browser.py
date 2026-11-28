"""Run the merged product's five browser fixtures in a loopback-only namespace."""
import hashlib
import json
import os
from pathlib import Path
import runpy
import socket
import subprocess
import sys

from playwright.sync_api import BrowserType, sync_playwright

product = Path(os.environ['PRODUCT_ROOT']).resolve()
receipt = Path(os.environ['RUNNER_TEMP']) / 'simpledb-receipt'
cases = [('scalar-read', 'simpledb_scalar_reference_live.py'),
         ('scalar-write', 'simpledb_shipped_examples_live.py'),
         ('console-one', 'simpledb_console.py'), ('console-two', 'simpledb_console.py'),
         ('console-three', 'simpledb_console.py')]


def check():
    assert {x for _, x in socket.if_nameindex()} == {'lo'} and os.getuid() != 0
    assert not any(k.startswith(('AWS_', 'BOTO_')) for k in os.environ)
    binary = product / 'dist/tenkacloud-simulator'
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    assert digest == os.environ['PRODUCT_DIGEST']
    assert subprocess.check_output(['git','-C', str(product), 'rev-parse','HEAD'],text=True).strip() == os.environ['PRODUCT_REVISION']
    assert not subprocess.check_output(['git','-C', str(product),'status','--porcelain'])
    return binary, digest


def one(filename):
    assert filename in {script for _,script in cases}
    original = BrowserType.launch
    def launch(self,*args,**kwargs):
        assert self.name == 'chromium' and not args and not kwargs.get('args') and not kwargs.get('ignore_default_args')
        kwargs['chromium_sandbox'] = True
        kwargs['args'] = ['--proxy-server=http://127.0.0.1:9','--proxy-bypass-list=127.0.0.1',
                          '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1']
        return original(self,**kwargs)
    BrowserType.launch = launch
    sys.path.insert(0,str(product/'test/e2e'))
    runpy.run_path(str(product/'test/e2e'/filename),run_name='__main__')


def run():
    binary, digest = check()
    with sync_playwright() as p:
        chrome = p.chromium.executable_path
    assert Path(chrome).is_file()
    env = dict(os.environ, SIMULATOR_BINARY=str(binary), SIMULATOR_BINARY_SHA256=digest, CHROMIUM_EXECUTABLE=chrome)
    for name, script in cases:
        folder = receipt / name
        folder.mkdir()
        result = subprocess.run([sys.executable, '-B', __file__, script], cwd=product,
                                env=dict(env,SIMULATOR_REPORTS=str(folder)),capture_output=True,text=True)
        if result.returncode:
            import re
            print(name, 'failed', re.findall(r'File "[^"\n]*/([A-Za-z0-9_.-]+)", line (\d+)',result.stderr)[-8:],
                  re.findall(r'^([\w.]+(?:Error|Exception))[:\n]',result.stderr,re.M)[-2:],flush=True)
            raise SystemExit(result.returncode)
        if name.startswith('console'):
            d=json.loads(result.stdout)
            assert d['simpledbConsole'] and d['liveReadback'] and d['mobileReflow']
            assert d['closedAndCleaned'] and d['roleDenied'] and d['conditionalWrites']==4 and d['realAWS'] is False
            (folder/'console.json').write_text(json.dumps(dict(d,binary_sha256=digest),indent=2)+'\n')
        else:
            files=list(folder.glob('*.json'));assert len(files)==1
            d=json.loads(files[0].read_text())
            assert d['binary_sha256']==digest and d['exactForm']
            if name=='scalar-read':
                expected=['ListDomains','DomainMetadata','GetAttributes']
                assert d['getAttributesMatchesNativeWriteIncludingEmptyValue'] and d['oneMatchedSendPerPresetNoRedirect']
                assert d['requestCookiePresent'] and d['liveXml'] and d['freshDomainDeleted']
            else:
                expected=['CreateDomain','Select','DeleteAttributes','PutAttributes','DeleteDomain']
                assert d['nativeReadback'] and d['oneCookieSendPerPreset'] and d['liveXmlAndRenderedResult'] and d['freshDomainsDeleted']
                assert d['putReplacesAllOldValuesAndPreservesPeers'] and d['targetedDeletePreservesPeerValue']
                assert d['deleteDomainPreservesOtherDomainAndItems']
            assert d['servedPresets']==expected and d['nativeStatuses']=={op:200 for op in expected}
        print(name, 'passed', flush=True)
    assert not subprocess.check_output(['git','-C',str(product),'status','--porcelain'])
    (receipt/'result.json').write_text(json.dumps({'revision':os.environ['PRODUCT_REVISION'],'digest':digest,
        'realScalarSends':8,'freshConsoleCases':3,'cleanupConfirmed':True},indent=2)+'\n')


if __name__=='__main__':
    if len(sys.argv)==2:
        check()
        one(sys.argv[1])
    else:
        run()
