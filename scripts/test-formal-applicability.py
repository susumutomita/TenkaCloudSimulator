"""Real temporary Git trees test exact binding, not semantic proof or review."""
import copy
import json
import shutil
import tempfile
import unittest
from pathlib import Path

from formal_applicability import GateError, RECORD, changes, disposition, git, initialize, load_registry, validate_record, exact_head_inputs

ROOT = Path(__file__).resolve().parents[1]


class ApplicabilityTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='formal-applicability-unit-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        shutil.copytree(ROOT / 'formal', self.root / 'formal')
        record = self.root / RECORD
        if record.exists():
            record.unlink()
        self.registry = json.loads((ROOT / 'formal/models.json').read_text())
        for path in {p for m in self.registry['models'] for p in m['source_scope']}:
            target = self.root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / path, target)
        for path in ('scripts/formal_model_gate.py', 'scripts/formal_applicability.py'):
            target = self.root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / path, target)
        git(self.root, 'init', '-q')
        git(self.root, 'config', 'user.name', 'Local Fixture')
        git(self.root, 'config', 'user.email', 'fixture@example.invalid')
        self.commit('baseline')
        self.base = git(self.root, 'rev-parse', 'HEAD').decode().strip()

    def commit(self, message):
        git(self.root, 'add', '.')
        git(self.root, 'commit', '-q', '-m', message)

    def prepare(self, path='internal/core/store.go'):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes((target.read_bytes() if target.exists() else b'') + b'\nfixture changed bytes\n')
        git(self.root, 'add', path)
        initialize(self.root, self.base)
        record = json.loads((self.root / RECORD).read_text())
        for item in record['changes']:
            model = next((m for m in self.registry['models'] if item['path'] in m['source_scope'] or item['path'] in (m['model'], m['config'])), None)
            item['reason'] = 'Explicit fixture argument; schema validation does not prove implementation semantics.'
            if model:
                item['disposition'] = 'modeled'
                item['models'] = [{'id': model['id'], 'properties': model['properties'], 'limits': model['limits']}]
            else:
                item['disposition'] = disposition(item['path']) or 'documentation'
        self.write_record(record)
        self.commit('record and implementation together')
        return record

    def write_record(self, record):
        (self.root / RECORD).write_text(json.dumps(record, indent=2) + '\n')

    def test_exact_modeled_change_and_known_documentation(self):
        self.prepare()
        evidence = validate_record(self.root, self.base)
        self.assertEqual(len(evidence['changes']), 1)
        self.assertIn('not-semantic-proof', evidence['kind'])
        path = self.root / 'docs/meaning.md'
        path.parent.mkdir(exist_ok=True)
        path.write_text('Documentation only')
        git(self.root, 'add', 'docs/meaning.md')
        initialize(self.root, self.base)
        record = json.loads((self.root / RECORD).read_text())
        for item in record['changes']:
            item['reason'] = 'The existing state binding is retained; this path is explanatory documentation.'
            item['disposition'] = 'documentation' if item['path'].startswith('docs/') else 'modeled'
            item['models'] = [] if item['path'].startswith('docs/') else evidence['changes'][0]['models']
        self.write_record(record)
        self.commit('expanded complete disposition')
        self.assertEqual(len(validate_record(self.root, self.base)['changes']), 2)

    def test_stale_blob_base_missing_and_extra_entries_fail(self):
        original = self.prepare()
        variants = []
        stale = copy.deepcopy(original); stale['changes'][0]['sha256'] = '0' * 64; variants.append(stale)
        stale = copy.deepcopy(original); stale['base_sha'] = '0' * 40; variants.append(stale)
        stale = copy.deepcopy(original); stale['changes'] = []; variants.append(stale)
        stale = copy.deepcopy(original); stale['changes'].append(copy.deepcopy(stale['changes'][0])); variants.append(stale)
        for index, record in enumerate(variants):
            self.write_record(record)
            self.commit('negative fixture ' + str(index))
            with self.assertRaises(GateError):
                validate_record(self.root, self.base)

    def test_updated_implementation_requires_new_digest(self):
        self.prepare()
        with (self.root / 'internal/core/store.go').open('ab') as output:
            output.write(b'next change')
        self.commit('stale applicability after source update')
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)

    def test_unknown_runtime_cannot_use_prose_or_unrelated_model(self):
        record = self.prepare('internal/aws/new_state.go')
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)
        record['changes'][0]['disposition'] = 'modeled'
        record['changes'][0]['models'] = [{'id': 'ObjectStore', 'properties': ['NoOrphans'], 'limits': 'Unrelated property cannot cover a new path.'}]
        self.write_record(record)
        self.commit('unrelated model is not applicable')
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)

    def test_unresolved_checkbox_fake_review_and_unchecked_property_fail(self):
        record = self.prepare()
        for index, mutation in enumerate(('empty-reason', 'reviewer', 'unchecked-property')):
            changed = copy.deepcopy(record)
            if mutation == 'empty-reason':
                changed['changes'][0]['reason'] = ''
            elif mutation == 'reviewer':
                changed['reviewed_by'] = 'not-authenticated-by-this-check'
            else:
                changed['changes'][0]['models'][0]['properties'] = ['CompleteGoCorrectness']
            self.write_record(changed)
            self.commit('not accepted proof marker ' + str(index))
            with self.assertRaises(GateError):
                validate_record(self.root, self.base)

    def test_record_itself_has_no_recursive_hash_and_new_paths_are_not_silently_ignored(self):
        self.prepare('docs/first.md')
        self.assertNotIn(RECORD, changes(self.root, self.base))
        (self.root / 'new-runtime.py').write_text('state = 1')
        self.commit('unrecorded path')
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)

    def test_registered_state_path_cannot_be_relabeled_as_documentation(self):
        record = self.prepare()
        record['changes'][0]['disposition'] = 'documentation'
        record['changes'][0]['models'] = []
        self.write_record(record)
        self.commit('state is not a docs exception')
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)

    def test_type_correctness_alone_is_not_a_state_safety_argument(self):
        record = self.prepare()
        record['changes'][0]['models'][0]['properties'] = ['TypeOK']
        self.write_record(record)
        self.commit('types alone are not an applicability argument')
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)

    def test_deleted_state_file_stays_bound_to_prior_model_scope(self):
        git(self.root, 'rm', 'internal/core/store.go')
        registry = copy.deepcopy(self.registry)
        for model in registry['models']:
            model['source_scope'] = [p for p in model['source_scope'] if p != 'internal/core/store.go']
        (self.root / 'formal/models.json').write_text(json.dumps(registry))
        git(self.root, 'add', 'formal/models.json')
        initialize(self.root, self.base)
        record = json.loads((self.root / RECORD).read_text())
        for item in record['changes']:
            item['reason'] = 'Deletion must remain reviewed against the previous declared storage safety contract.'
            if item['path'] == 'formal/models.json':
                item['disposition'] = 'formal-infrastructure'
            else:
                self.assertIsNone(item['sha256'])
                item['disposition'] = 'modeled'
                item['models'] = [{'id':'ObjectStore','properties':['NoOrphans'],'limits':'Checks the finite contract, not a proof of deleted Go implementation.'}]
        self.write_record(record)
        self.commit('explicitly bound deletion')
        validate_record(self.root, self.base)

    def test_dirty_and_staged_inputs_do_not_validate_committed_receipts(self):
        self.prepare()
        for path in ('formal/models.json', 'formal/ObjectStore.tla', 'formal/ObjectStore.cfg', 'scripts/formal_applicability.py', 'internal/core/store.go'):
            with self.subTest(path=path):
                target = self.root / path
                original = target.read_bytes()
                target.write_bytes(original + b'\nchanged after HEAD\n')
                with self.assertRaises(GateError):
                    validate_record(self.root, self.base)
                git(self.root, 'add', path)
                target.write_bytes(original)  # Worktree restored; index still differs.
                with self.assertRaises(GateError):
                    validate_record(self.root, self.base)
                git(self.root, 'reset', '-q', 'HEAD', '--', path)
        validate_record(self.root, self.base)

    def test_untracked_and_ignored_policy_model_inputs_fail(self):
        self.prepare()
        target = self.root / 'new_state.go'
        target.write_text('uncommitted implementation')
        with self.assertRaises(GateError):
            exact_head_inputs(self.root)
        target.unlink()
        ignored = self.root / 'scripts/ignored.py'
        (self.root / '.git/info/exclude').write_text('scripts/ignored.py\n')
        ignored.write_text('untracked policy replacement')
        with self.assertRaises(GateError):
            exact_head_inputs(self.root)

    def test_prior_presentation_state_scope_cannot_be_deleted_and_relabeled(self):
        path = 'tools/console/src/state.js'
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text('previously modeled presentation state')
        registry_path = self.root / 'formal/models.json'
        registry = json.loads(registry_path.read_text())
        registry['models'][0]['source_scope'].append(path)
        registry_path.write_text(json.dumps(registry))
        self.commit('prior declared UI state scope')
        self.base = git(self.root, 'rev-parse', 'HEAD').decode().strip()
        git(self.root, 'rm', path)
        registry['models'][0]['source_scope'].remove(path)
        registry_path.write_text(json.dumps(registry))
        git(self.root, 'add', 'formal/models.json')
        initialize(self.root, self.base)
        record = json.loads((self.root / RECORD).read_text())
        for item in record['changes']:
            item['disposition'] = disposition(item['path'])
            item['reason'] = 'A previous state scope cannot be erased by relabeling this deletion as UI.'
        self.write_record(record)
        self.commit('scope erasure is not an exception')
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)


if __name__ == '__main__':
    unittest.main()
