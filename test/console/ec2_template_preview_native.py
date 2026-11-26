"""Opt-in signed-in EC2 UI check with the real localhost CLI and JSDOM.

Use SIMULATOR_BINARY and JSDOM_MODULE (an installed jsdom module or absolute
module path). This does not run Chromium or launch anything.
"""
import json
import os
from pathlib import Path
import subprocess

from ec2_launch_preflight_native import Local, run_default, state


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization')
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    with run_default(binary) as (port, token):
        local = Local(port)
        operator = local.session(token)
        reviewer = local.preview_only_session(operator)
        _, issued = local.call('POST', '/_simulator/v1/access', json.dumps({'Principal': 'no-template-preview'}),
                               Cookie=operator, **{'Content-Type': 'application/json'})
        denied = local.session(json.loads(issued)['AccessToken'])
        before = state(local, operator)
        subprocess.run(['node', str(Path(__file__).with_name('ec2_template_preview_dom.cjs'))],
                       input=json.dumps(dict(port=port, operator=operator, reviewer=reviewer, denied=denied)),
                       text=True, check=True, timeout=60)
        assert state(local, operator) == before, 'Preview changed visible stack, VPC or instance state'
    print('PASS no visible persisted state changed; isolated native simulator cleaned up.')
