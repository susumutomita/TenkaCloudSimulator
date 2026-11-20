#!/usr/bin/env python3
"""Fixed leaf production-binding runner; models/controls cannot be product receipts."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys

HERE=Path(__file__).resolve().parent
ROOT=HERE.parents[3]
PACKAGE='github.com/susumutomita/TenkaCloudSimulator/test/factory/testdata/simpledb-leaf/binding'
TEST='TestSimpleDBLeafProductionContract'
EXPECTED_INPUTS={'README.md','binding/subject_test.go','contract/contract.go','contract/control_test.go','contract/vectors.json','oracle.json','registration-proposal.json','root-adapter-negative.json','runner.py','test_runner.py'}
REQUIRED=['domain-lifecycle','item-workflow','restart-cascade','list-boundaries','rejected-write-atomicity','scope-isolation','cancelled-write']

def verify_inputs():
    manifest=json.loads((HERE/'files.sha256.json').read_text())
    if set(manifest.get('files',{}))!=EXPECTED_INPUTS:raise ValueError('missing/extra immutable input')
    for name,want in manifest['files'].items():
        p=HERE/name
        if Path(name).is_absolute() or '..' in Path(name).parts or p.is_symlink() or not p.resolve().is_relative_to(HERE.resolve()) or hashlib.sha256(p.read_bytes()).hexdigest()!=want:
            raise ValueError('changed/unsafe TestWriter input: '+name)

def classify(log,returncode):
    try:events=[json.loads(line) for line in log.splitlines() if line.strip()]
    except (ValueError,TypeError):return 'HARNESS_ERROR',2
    if not events or not all(isinstance(e,dict) for e in events):return 'HARNESS_ERROR',2
    text=''.join(e.get('Output','') for e in events)
    builds=[e for e in events if e.get('Action')=='build-fail']
    if builds:
        own={PACKAGE,'github.com/susumutomita/TenkaCloudSimulator/internal/aws/simpledb'}
        return ('BINDING_UNESTABLISHED',2) if all(e.get('ImportPath') in own for e in builds) else ('HARNESS_ERROR',2)
    if 'test timed out after' in text or any(e.get('Action')=='skip' for e in events):return 'HARNESS_ERROR',2
    reports=[]
    for e in events:
        output=e.get('Output','')
        if 'LEAF_CONTRACT_REPORT ' in output:
            try:reports.append(json.loads(output.split('LEAF_CONTRACT_REPORT ',1)[1].strip()))
            except ValueError:return 'HARNESS_ERROR',2
    if len(reports)!=1:return 'HARNESS_ERROR',2
    result=reports[0]
    if result.get('surface')!='leaf-store-contract-only':return 'HARNESS_ERROR',2
    cases=result.get('cases',[])
    if not isinstance(cases,list) or not all(isinstance(c,dict) for c in cases):return 'HARNESS_ERROR',2
    if [c.get('id') for c in cases]!=REQUIRED:return 'HARNESS_ERROR',2
    statuses=[c.get('status') for c in cases]
    if not set(statuses)<={'PASS','BINDING_UNESTABLISHED','HARNESS_ERROR','PRODUCT_CONTRACT_RED'}:return 'HARNESS_ERROR',2
    def count(name,action):return sum(e.get('Package')==PACKAGE and e.get('Test')==name and e.get('Action')==action for e in events)
    if count(TEST,'run')!=1:return 'HARNESS_ERROR',2
    for case in cases:
        name=TEST+'/'+case['id'];expected='pass' if case['status']=='PASS' else 'fail'
        if count(name,'run')!=1 or count(name,expected)!=1:return 'HARNESS_ERROR',2
    if 'BINDING_UNESTABLISHED' in statuses:return 'BINDING_UNESTABLISHED',2
    if 'HARNESS_ERROR' in statuses:return 'HARNESS_ERROR',2
    if 'PRODUCT_CONTRACT_RED' in statuses:
        return ('PRODUCT_CONTRACT_RED',1) if returncode!=0 and count(TEST,'fail')==1 else ('HARNESS_ERROR',2)
    if returncode!=0 or count(TEST,'pass')!=1 or any(e.get('Action')=='fail' for e in events):return 'HARNESS_ERROR',2
    if sum(e.get('Package')==PACKAGE and not e.get('Test') and e.get('Action')=='pass' for e in events)!=1:return 'HARNESS_ERROR',2
    return 'PASS',0

def snapshot():
    env={k:v for k,v in os.environ.items() if not k.startswith('GIT_')}
    head=subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,env=env,text=True).strip()
    dirty=subprocess.check_output(['git','status','--porcelain','--untracked-files=all'],cwd=ROOT,env=env,text=True).strip()
    if dirty:raise ValueError('dirty inputs cannot be product acceptance')
    return head

def execute(go):
    go=shutil.which(go)
    if not go:raise ValueError('configured Go executable missing')
    go=str(Path(go).resolve())
    env={k:v for k,v in os.environ.items() if k in ('PATH','HOME','TMPDIR','GOCACHE','GOMODCACHE','GOPATH','SYSTEMROOT')}
    env.update(GOROOT=str(Path(go).parent.parent),GOENV='off',GOWORK='off',GOFLAGS='',GOTOOLCHAIN='local',GOPROXY='off',GOSUMDB='off',GOMAXPROCS='2',CGO_ENABLED='0')
    argv=[go,'test','-p=1','-json','-count=1','-timeout=60s','./test/factory/testdata/simpledb-leaf/binding']
    p=subprocess.Popen(argv,cwd=ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
    try:
        stdout,stderr=p.communicate(timeout=75)
        return stdout.decode(),stderr.decode(),p.returncode,argv
    finally:
        try:os.killpg(p.pid,signal.SIGKILL)
        except ProcessLookupError:pass
        p.wait(timeout=3)

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--go',default='go');parser.add_argument('--report',required=True,type=Path);parser.add_argument('--log',required=True,type=Path);args=parser.parse_args()
    if args.report.resolve()==args.log.resolve() or any(p.resolve().is_relative_to(ROOT.resolve()) for p in (args.report,args.log)):
        print(json.dumps({'status':'HARNESS_ERROR','reason':'distinct external output paths required'}));return 2
    for p in (args.report,args.log):p.parent.mkdir(parents=True,exist_ok=True)
    report={'surface':'leaf-production-store-contract','status':'HARNESS_ERROR','actual_cases_run':0,'qualification':'Not registered factory evidence; never SDK/server/IAM acceptance'};code=2
    try:
        verify_inputs()
        report['head']=snapshot()
        if not (ROOT/'internal/aws/simpledb/store.go').is_file():
            report.update(status='UNIMPLEMENTED_RED',binding='not built: production store absent',reason='No production store exists. This is presence evidence only; no contract behavior was executed.')
            code=1;args.log.write_text('')
        else:
            log,stderr,rc,argv=execute(args.go);args.log.write_text(log+stderr);status,code=classify(log,rc);report.update(status=status,argv=argv,returncode=rc)
            events=[json.loads(line) for line in log.splitlines() if line.strip()]
            report['actual_cases_run']=sum(e.get('Action')=='run' and e.get('Package')==PACKAGE and e.get('Test') in [TEST+'/'+c for c in REQUIRED] for e in events)
        verify_inputs()
        if snapshot()!=report['head']:raise ValueError('inputs changed during acceptance')
    except Exception as error:
        report['status']='HARNESS_ERROR';code=2
        report['reason']=type(error).__name__+': '+str(error)
    args.report.write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report));return code
if __name__=='__main__':sys.exit(main())
