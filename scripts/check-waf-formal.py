"""Independent bounded WAF model and five sensitivity mutations.

No claim of implementation refinement. Does not alter the shared formal gate.
All TLC working files and logs live below --output (never the system temp dir).
"""
import argparse
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output', type=Path, default=Path('reports/waf-formal'))
args = parser.parse_args()
repo = Path(__file__).resolve().parents[1]
jar = Path(os.environ['TLA_JAR']).resolve()
expected = (repo / 'formal/tla2tools.sha256').read_text().split()[0]
if hashlib.sha256(jar.read_bytes()).hexdigest() != expected:
    raise SystemExit('TLC jar checksum mismatch')
output = args.output.resolve()
output.mkdir(parents=True, exist_ok=True)
original = (repo / 'formal/WAFRequestEvaluation.tla').read_text()
config = (repo / 'formal/WAFRequestEvaluation.cfg').read_text()
mutations = [
    ('CountTerminates',
     '/\\ counted\' = Append(counted, CurrentRule) /\\ pc\' = pc + 1',
     '/\\ counted\' = Append(counted, CurrentRule) /\\ pc\' = pc + 1 /\\ phase\' = "done"',
     ('FirstTerminatingRuleWins',)),
    ('ReversedOrder',
     'CurrentRule == RuleAt(pc)',
     'CurrentRule == RuleAt(RuleCount + 1 - pc)',
     ('CountContinues', 'FirstTerminatingRuleWins')),
    ('EarlyDefault',
     'FinishDefault == /\\ phase = "evaluating" /\\ pc > RuleCount',
     'FinishDefault == /\\ phase = "evaluating" /\\ pc >= RuleCount',
     ('FirstTerminatingRuleWins',)),
    ('DeleteAssociatedACL',
     'DeleteACL == /\\ aclExists /\\ ~associated',
     'DeleteACL == /\\ aclExists /\\ TRUE',
     ('NoAssociationOrphans',)),
    ('DeleteAssociatedStage',
     'DeleteStage == /\\ stageExists /\\ ~associated',
     'DeleteStage == /\\ stageExists /\\ TRUE',
     ('NoAssociationOrphans',)),
]
# Mutate actions, never the independent reference oracle. The Count action must
# drop phase from UNCHANGED when assigning it, or TLC merely sees an empty action.
variants = [('WAFRequestEvaluation', original, ())]
for suffix, old, new, invariants in mutations:
    if original.count(old) != 1:
        raise SystemExit('Mutation anchor drift: ' + suffix)
    name = 'WAFRequestEvaluation' + suffix
    source = original.replace('MODULE WAFRequestEvaluation ', 'MODULE ' + name + ' ')
    source = source.replace(old, new)
    if suffix == 'CountTerminates':
        old_unchanged = '/\\ UNCHANGED <<configuration, lifecycle, phase, snapshotAssociated,\n                            decision, terminating>>'
        if source.count(old_unchanged) != 1:
            raise SystemExit('Count mutation unchanged anchor drift')
        source = source.replace(old_unchanged, '/\\ UNCHANGED <<configuration, lifecycle, snapshotAssociated,\n                            decision, terminating>>')
    variants.append((name, source, invariants))

with tempfile.TemporaryDirectory(prefix='tlc-work-', dir=output) as directory:
    work = Path(directory)
    for name, source, invariants in variants:
        (work / (name + '.tla')).write_text(source)
        (work / (name + '.cfg')).write_text(config)
        run = subprocess.run(
            ['java', '-Xmx1024m', '-XX:+UseParallelGC', '-Djava.io.tmpdir=' + str(work), '-cp', str(jar), 'tlc2.TLC', '-workers', '1',
             '-metadir', str(work / (name + '-states')), '-config',
             str(work / (name + '.cfg')), str(work / (name + '.tla'))],
            capture_output=True, text=True, timeout=60)
        text = run.stdout + run.stderr
        (output / (name + '.txt')).write_text(text)
        if not invariants:
            if run.returncode != 0 or 'No error has been found' not in text:
                raise SystemExit(name + ' failed; inspect ' + str(output / (name + '.txt')))
            print(name + ': bounded invariants hold', flush=True)
        else:
            detected = [inv for inv in invariants if 'Invariant ' + inv + ' is violated' in text]
            if run.returncode == 0 or not detected:
                raise SystemExit(name + ' mutation not detected by intended invariant; inspect ' +
                                 str(output / (name + '.txt')))
            print(name + ': detected by ' + ', '.join(detected), flush=True)
