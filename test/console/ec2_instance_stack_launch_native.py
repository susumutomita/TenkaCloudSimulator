"""Test embedded launch controls and real refusal on an authenticated local CLI."""
import json
import os
from pathlib import Path
import subprocess

from ec2_launch_preflight_native import Local, run_default, state


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization')
    with run_default(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)) as (port, token):
        local = Local(port)
        operator = local.session(token)
        participant = local.preview_only_session(operator)
        before = state(local, operator)
        subprocess.run(['node', str(Path(__file__).with_name('ec2_instance_stack_launch_dom.cjs'))],
                       input=json.dumps(dict(port=port, operator=operator, participant=participant)),
                       text=True, check=True, timeout=60)
        assert state(local, operator) == before, 'Refused launch left observable state'
    print('PASS unchanged regional stack/VPC/instance inventories and isolated simulator cleanup.')
