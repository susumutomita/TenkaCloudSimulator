#!/usr/bin/env python3
"""Fixed SDK factory entrypoint; prerequisite failure never satisfies acceptance."""
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]

def interrupted(signum, frame):
    raise TimeoutError('SDK executor interrupted; owned CLI cleanup required')

def run_owned_process(argv, cwd, timeout, stdout=subprocess.DEVNULL):
    # A compiler can outlive the Go leader. Always terminate the owned session,
    # including descendants, rather than killing only that leader on timeout.
    process = subprocess.Popen(argv, cwd=cwd, stdout=stdout, stderr=subprocess.DEVNULL,
                               start_new_session=True)
    try:
        returncode = process.wait(timeout=timeout)
        if returncode:
            raise subprocess.CalledProcessError(returncode, argv)
    finally:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=3)


def main():
    report = {'factory_sdk_v1': True, 'head': os.environ.get('FACTORY_HEAD'),
              'status': 'HARNESS_ERROR', 'passed_cases': [], 'python': os.environ.get('FACTORY_PYTHON'), 'python_prefix': sys.prefix,
              'go': os.environ.get('FACTORY_GO')}
    code = 2
    try:
        signal.signal(signal.SIGTERM, interrupted)
        signal.signal(signal.SIGINT, interrupted)
        # Import only the immutable TestWriter harness, not implementation code.
        path = ROOT/'test/factory/testdata/simpledb/sdk_wire.py'
        spec = importlib.util.spec_from_file_location('simpledb_testwriter_sdk', path)
        wire = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(wire)
        report.update(required_cases=wire.REQUIRED, sdk_pins=wire.PINS)
        if not report['python'] or not Path(report['python']).is_absolute() or not Path(report['python']).samefile(sys.executable):
            raise ValueError('executor-owned Python invocation required')
        sdk = wire.preflight()  # No build or CLI start if fixed SDK is unavailable.
        go = report['go']
        if not go or not Path(go).is_absolute() or not Path(go).is_file():
            raise ValueError('executor-owned absolute Go tool required')
        import factory_acceptance as gate
        if not report['head'] or gate.clean_head(ROOT) != report['head']:
            raise ValueError('SDK builder is not at executor clean HEAD')
        with tempfile.TemporaryDirectory(prefix='simpledb-factory-build-') as directory:
            binary = Path(directory)/'simulator'
            run_owned_process([go, 'build', '-trimpath', '-o', str(binary), './cmd/simulator'], ROOT, 120)
            report['binary_sha256'] = gate.digest(binary.read_bytes())
            wire.execute(binary, report, sdk)
            wire.require(report['passed_cases'] == wire.REQUIRED, 'required SDK cases missing')
            if report.get('cleanup_errors'):
                raise RuntimeError('owned CLI cleanup failed')
            if gate.clean_head(ROOT) != report['head']:
                raise ValueError('source changed during SDK acceptance')
            report['status'], code = 'PASS', 0
    except Exception as error:
        report['reason'] = f'{type(error).__name__}: {error}'
        if 'wire' in locals() and isinstance(error, wire.ProductFailure):
            report['status'], code = 'PRODUCT_CONTRACT_RED', 1
    if report.get('cleanup_errors'):
        report['cleanup_status'], code = 'FAILED', 2
    print(json.dumps(report, sort_keys=True))
    return code

if __name__ == '__main__':
    sys.exit(main())
