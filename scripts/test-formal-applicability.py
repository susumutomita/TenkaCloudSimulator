"""Real temporary Git trees test exact binding, not semantic proof or review."""
import copy
import hashlib
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import formal_applicability

from formal_applicability import GateError, RECORD, changes, disposition, git, initialize, load_registry, validate_record, exact_head_inputs

ROOT = Path(__file__).resolve().parents[1]


class SmithyFixtureClassificationTests(unittest.TestCase):
    def test_only_selected_smithy_sources_are_test_support(self):
        for name in ('LICENSE.txt', 'NOTICE.txt', 'source.json', 'sqs.json', 'sqs-contracts.json'):
            self.assertEqual(disposition('internal/aws/testdata/smithy/' + name), 'test-support')
        self.assertEqual(disposition('scripts/smithy-contracts.py'), 'validation-tooling')
        for path in ('internal/aws/testdata/smithy/runtime.go',
                     'internal/aws/testdata/smithy/sqs.json.go',
                     'internal/aws/testdata/smithy/unknown.json',
                     'internal/aws/smithy.go',
                     'scripts/smithy-runtime.py'):
            self.assertIsNone(disposition(path), path)


class LandingPresentationBoundaryTests(unittest.TestCase):
    def test_only_named_static_landing_documents_are_presentation(self):
        for path in ('internal/server/landing/index.html', 'internal/server/landing/landing.css'):
            self.assertEqual(disposition(path), 'presentation')
        for path in ('internal/server/landing/landing.mjs',
                     'internal/server/landing/topology-canvas.mjs',
                     'internal/server/landing/topology-data.mjs',
                     'internal/server/landing/unknown.html',
                     'internal/server/landing/unknown.css',
                     'internal/server/landing.go'):
            self.assertIsNone(disposition(path), path)


class PromptDocumentationClassificationTests(unittest.TestCase):
    def test_only_prompt_markdown_categories_are_documentation(self):
        for path in ('CLAUDE.md', '.claude/agents/code-reviewer.md',
                     '.claude/rules/test-authoring.md', '.claude/skills/feature/SKILL.md'):
            self.assertEqual(disposition(path), 'documentation', path)

    def test_runnable_hooks_settings_and_security_controls_are_not_exempt(self):
        for path in ('CLAUDE.py', '.claude/settings.json', '.claude/settings.local.json',
                     '.claude/hooks/pre-tool.sh', '.claude/hooks/guard.py',
                     '.claude/hooks/policy.md', '.claude/permissions.json',
                     '.claude/agents/code-reviewer.py', '.claude/rules/policy.json',
                     '.claude/skills/feature/run.py', '.claude/skills/feature/run.sh',
                     '.claude/skills/feature/SKILL.md.py',
                     '.claude/agents/hooks/policy.md', '.claude/rules/hooks/policy.md',
                     '.claude/skills/feature/hooks/SKILL.md', 'internal/server/access.go'):
            self.assertIsNone(disposition(path), path)


class ReleaseClassificationTests(unittest.TestCase):
    def test_release_validation_classification_is_exact(self):
        for name in ('build-release.py', 'verify-release.py', 'verify-release-tag.py'):
            self.assertEqual(disposition('scripts/' + name), 'validation-tooling')
        for path in ('scripts/build-release-other.py', 'scripts/verify-release-wrapper.py',
                     'internal/aws/release.go'):
            self.assertIsNone(disposition(path), path)


class STSFixtureClassificationTests(unittest.TestCase):
    def test_exact_hello_battle_iam_fixture_classification(self):
        self.assertEqual(disposition('internal/aws/testdata/hello-world-battle-iam-fragment.yaml'), 'test-support')
        for path in ('internal/aws/testdata/hello-world-battle-iam-fragment.go',
                     'internal/aws/testdata/hello-world-battle-other.yaml',
                     'internal/aws/testdata/hello-world-battle-iam-fragment.yaml.go',
                     'internal/aws/hello-world-battle-iam-fragment.yaml'):
            self.assertIsNone(disposition(path))

    def test_exact_sts_fixture_and_helper_classification(self):
        self.assertEqual(disposition('scripts/check-sts-formal-candidate.py'), 'validation-tooling')
        self.assertEqual(disposition('internal/aws/testdata/sts-original/hello-world.yaml'), 'test-support')
        for name in ('README.md', 'LICENSE'):
            self.assertEqual(disposition('internal/aws/testdata/sts-original/' + name), 'documentation')
        for path in ('scripts/check-sts-other.py',
                     'internal/aws/testdata/sts-original/other.yaml',
                     'internal/aws/testdata/sts-original/hello-world.go',
                     'internal/aws/testdata/sts-original/helper.py',
                     'internal/aws/sts_roles.go', 'internal/server/access_sdk.go'):
            self.assertIsNone(disposition(path), path)


class ApplicabilityTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='formal-applicability-unit-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        shutil.copytree(ROOT / 'formal', self.root / 'formal')
        for path in ('go.mod', 'go.sum'):
            shutil.copyfile(ROOT / path, self.root / path)
        shutil.copyfile(ROOT / '.gitignore', self.root / '.gitignore')
        record = self.root / RECORD
        if record.exists():
            record.unlink()
        self.registry = json.loads((ROOT / 'formal/models.json').read_text())
        for path in {p for m in self.registry['models'] for p in m['source_scope']}:
            target = self.root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / path, target)
        for path in ('scripts/formal_model_gate.py', 'scripts/formal_applicability.py', 'scripts/formal_module_metadata.py'):
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

    def test_dynamodb_optional_sdk_fixture_classification_is_exact(self):
        self.assertEqual(disposition('internal/aws/testdata/dynamodb_size_sdk.py'), 'test-support')
        for path in ('internal/aws/testdata/other_sdk.py',
                     'internal/aws/testdata/dynamodb_size_sdk.go',
                     'internal/aws/dynamodb_size.go',
                     'internal/aws/dynamodb_number.go',
                     'internal/aws/dynamodb_store.go'):
            self.assertIsNone(disposition(path), path)

    def test_japanese_readme_is_bound_without_accepting_runtime_lookalikes(self):
        self.prepare('README.ja.md')
        evidence = validate_record(self.root, self.base)
        self.assertEqual(evidence['changes'][0]['path'], 'README.ja.md')
        self.assertEqual(evidence['changes'][0]['disposition'], 'documentation')
        self.prepare('README.ja.go')
        with self.assertRaisesRegex(GateError, 'unclassified implementation/state path'):
            validate_record(self.root, self.base)

    def test_prompt_markdown_is_bound_and_settings_cannot_claim_documentation(self):
        self.prepare('CLAUDE.md')
        evidence = validate_record(self.root, self.base)
        self.assertEqual(evidence['changes'][0]['disposition'], 'documentation')
        self.prepare('.claude/skills/feature/SKILL.md')
        self.assertEqual(len(validate_record(self.root, self.base)['changes']), 2)
        self.prepare('.claude/settings.json')
        with self.assertRaisesRegex(GateError, 'unclassified implementation/state path'):
            validate_record(self.root, self.base)

    def test_prompt_docs_classification_does_not_override_registered_state_scope(self):
        path = '.claude/agents/code-reviewer.md'
        record = self.prepare(path)
        registry = copy.deepcopy(self.registry)
        registry['models'][0]['source_scope'].append(path)
        with self.assertRaisesRegex(GateError, 'declared state/model scope requires'):
            validate_record(self.root, self.base, registry)

    def test_prompt_markdown_digest_cannot_be_stale(self):
        self.prepare('CLAUDE.md')
        (self.root / 'CLAUDE.md').write_text('Different prompt contract')
        self.commit('changed prompt without updated binding')
        with self.assertRaisesRegex(GateError, 'stale applicability blob'):
            validate_record(self.root, self.base)

    def test_nested_prompt_path_cannot_claim_documentation(self):
        self.prepare('.claude/skills/feature/hooks/SKILL.md')
        with self.assertRaisesRegex(GateError, 'unclassified implementation/state path'):
            validate_record(self.root, self.base)

    def test_executable_prompt_path_is_not_documentation(self):
        self.prepare('CLAUDE.md')
        (self.root / 'CLAUDE.md').chmod(0o755)
        self.commit('executable prompt lookalike')
        with self.assertRaisesRegex(GateError, 'prompt documentation must be regular nonexecutable'):
            validate_record(self.root, self.base)

    def test_deleted_executable_prompt_requires_explicit_policy_work(self):
        target = self.root / 'CLAUDE.md'
        target.write_text('#!/bin/sh\nexit 0\n')
        target.chmod(0o755)
        self.commit('executable file existed before change')
        self.base = git(self.root, 'rev-parse', 'HEAD').decode().strip()
        target.unlink()
        git(self.root, 'add', 'CLAUDE.md')
        initialize(self.root, self.base)
        record = json.loads((self.root / RECORD).read_text())
        record['changes'][0].update(disposition='documentation', reason='Removal does not bypass mode checks.')
        self.write_record(record)
        self.commit('attempt to relabel executable removal as documentation')
        with self.assertRaisesRegex(GateError, 'prompt documentation must be regular nonexecutable'):
            validate_record(self.root, self.base)

    def test_symlink_prompt_cannot_claim_documentation(self):
        self.prepare('CLAUDE.md')
        target = self.root / 'CLAUDE.md'
        target.unlink()
        target.symlink_to('go.mod')
        self.commit('symlink prompt lookalike')
        with self.assertRaisesRegex(GateError, 'symlink/submodule changes require explicit policy work'):
            validate_record(self.root, self.base)

    def test_native_provenance_validator_classification_is_exact(self):
        self.prepare('scripts/check-native-provenance.py')
        evidence = validate_record(self.root, self.base)
        self.assertEqual(evidence['changes'][0]['disposition'], 'validation-tooling')
        self.assertIsNone(disposition('scripts/check-native-provenance-wrapper.py'))
        self.prepare('internal/aws/check-native-provenance.go')
        with self.assertRaisesRegex(GateError, 'unclassified implementation/state path'):
            validate_record(self.root, self.base)

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

    def test_cli_generated_evidence_does_not_dirty_the_source_tree(self):
        self.prepare()
        result = subprocess.run([sys.executable, str(ROOT / 'scripts/check-formal-applicability.py'), 'check', '--base', self.base], cwd=self.root, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.root / 'reports/formal/applicability.json').is_file())
        validate_record(self.root, self.base)


    def module_baseline(self):
        self.module = b'module example.com/fixture\n\ngo 1.26.0\n\nrequire (\n\texample.com/a v1.2.3\n\texample.com/b v2.3.4 // indirect\n)\n'
        (self.root / 'go.mod').write_bytes(self.module)
        (self.root / 'go.sum').write_bytes(b'fixture-checksums-unchanged\n')
        self.commit('module baseline')
        self.base = git(self.root, 'rev-parse', 'HEAD').decode().strip()
        return self.module.replace(b'\texample.com/b v2.3.4 // indirect', b'\texample.com/b v2.3.4')

    def module_record(self, after):
        (self.root / 'go.mod').write_bytes(after)
        git(self.root, 'add', '.')
        initialize(self.root, self.base)
        record = json.loads((self.root / RECORD).read_text())
        for item in record['changes']:
            item.update(disposition='build-metadata', reason='Require set and other directives unchanged; no token-state invariant is claimed.', models=[])
        self.write_record(record)
        self.commit('module annotation candidate')

    def test_module_indirect_and_grouping_metadata_only(self):
        after = self.module_baseline()
        # Merge blocks into individual directives and change order/annotation.
        after = b'module example.com/fixture\n\ngo 1.26.0\n\nrequire example.com/b v2.3.4\nrequire example.com/a v1.2.3 // indirect\n'
        self.module_record(after)
        self.assertEqual(validate_record(self.root, self.base)['changes'][0]['disposition'], 'build-metadata')
        self.assertIsNone(disposition('go.mod'))  # Not a general allowlist.

    def test_modeled_dependency_annotation_is_metadata_but_versions_are_not(self):
        registry = json.loads((self.root / 'formal/models.json').read_text())
        self.assertTrue(any('go.mod' in model['source_scope'] for model in registry['models']))
        after = self.module_baseline()
        self.module_record(after)
        self.assertEqual(validate_record(self.root, self.base)['changes'][0]['disposition'], 'build-metadata')
        self.module_record(after.replace(b'v1.2.3', b'v1.2.4'))
        with self.assertRaisesRegex(GateError, 'not a recognized require-annotation'):
            validate_record(self.root, self.base)

    def test_module_semantic_and_unknown_changes_are_rejected(self):
        after = self.module_baseline()
        variants = {
            'version': after.replace(b'v1.2.3', b'v1.2.4'),
            'added': after.replace(b')\n', b'\texample.com/c v1.0.0\n)\n'),
            'deleted': after.replace(b'\texample.com/a v1.2.3\n', b''),
            'go': after.replace(b'go 1.26.0', b'go 1.26.1'),
            'toolchain': after.replace(b'go 1.26.0', b'go 1.26.0\ntoolchain go1.26.1'),
            'replace': after + b'replace example.com/a => ./local\n',
            'exclude': after + b'exclude example.com/a v1.2.3\n',
            'retract': after + b'retract v1.0.0\n',
            'unknown': after + b'tool example.com/a/tool\n',
            'duplicate': after.replace(b')\n', b'\texample.com/a v1.2.3\n)\n'),
            'quoted': after.replace(b'example.com/a', b'"example.com/a"'),
            'comment': after + b'// other comment\n',
            'semantic-comment': after + b'//go:debug panicnil=1\n',
            'unknown-header-comment': b'// note\n' + after,
            'unicode': after.replace(b'example.com/a', 'example.com/é'.encode()),
            'prerelease': after.replace(b'v1.2.3', b'v1.2.3-rc1'),
            'empty-block': after + b'require (\n)\n',
            'unclosed-block': after + b'require (\n',
            'inline': after.replace(b'require (\n', b'require ( '),
            'crlf': after.replace(b'\n', b'\r\n'),
        }
        for kind, changed in variants.items():
            with self.subTest(kind=kind):
                self.module_record(changed)
                with self.assertRaises(GateError):
                    validate_record(self.root, self.base)

    def test_module_changed_or_missing_sum_and_symlink_fail(self):
        after = self.module_baseline()
        self.module_record(after)
        (self.root / 'go.sum').write_bytes(b'changed checksums\n')
        self.module_record(after)
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)
        git(self.root, 'rm', 'go.sum')
        self.module_record(after)
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)
        (self.root / 'go.sum').symlink_to('go.mod')
        with self.assertRaises(GateError):
            self.module_record(after)

    def test_module_dirty_staged_and_unrelated_runtime_cannot_be_exempted(self):
        after = self.module_baseline()
        self.module_record(after)
        (self.root / 'go.mod').write_bytes(self.module)
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)
        git(self.root, 'add', 'go.mod')
        (self.root / 'go.mod').write_bytes(after)
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)
        git(self.root, 'reset', '-q', 'HEAD', '--', 'go.mod')
        (self.root / 'go.mod').write_bytes(after)
        record = self.prepare('internal/aws/unmodeled_module_user.go')
        for item in record['changes']:
            item.update(disposition='build-metadata', models=[])
        self.write_record(record)
        self.commit('module exception does not exempt Go source')
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)



    def test_original_startup_module_blobs_and_negative_controls(self):
        fixture = json.loads((ROOT / 'test/formal/startup-module-metadata.json').read_text())
        for key in ('before', 'after', 'go_sum'):
            self.assertEqual(hashlib.sha256(fixture[key].encode()).hexdigest(), fixture['sha256'][key])
        (self.root / 'go.mod').write_text(fixture['before'])
        (self.root / 'go.sum').write_text(fixture['go_sum'])
        git(self.root, 'add', 'go.mod', 'go.sum')
        # Current formal baseline may already contain these exact public blobs.
        git(self.root, 'commit', '-q', '--allow-empty', '-m', 'original public Startup308 module baseline')
        self.base = git(self.root, 'rev-parse', 'HEAD').decode().strip()
        self.module_record(fixture['after'].encode())
        result = validate_record(self.root, self.base)
        self.assertEqual([(c['path'], c['disposition'], c['models']) for c in result['changes']], [('go.mod', 'build-metadata', [])])
        self.module_record(fixture['after'].replace('v0.44.0', 'v0.45.0').encode())
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)
        (self.root / 'go.sum').write_text(fixture['go_sum'] + 'changed checksum fixture\n')
        self.module_record(fixture['after'].encode())
        with self.assertRaises(GateError):
            validate_record(self.root, self.base)



    def test_git_flags_cannot_hide_module_or_sum_bytes(self):
        after = self.module_baseline()
        self.module_record(after)
        before = exact_head_inputs(self.root)
        self.assertTrue({'go.mod', 'go.sum'} <= set(before['hashes']))
        for flag, undo in (('--assume-unchanged', '--no-assume-unchanged'), ('--skip-worktree', '--no-skip-worktree')):
            for path in ('go.mod', 'go.sum'):
                with self.subTest(flag=flag, path=path):
                    target = self.root / path
                    original = target.read_bytes()
                    git(self.root, 'update-index', flag, path)
                    target.write_bytes(original + b'\nchanged but hidden by Git flag\n')
                    self.assertEqual(git(self.root, 'status', '--porcelain'), b'')
                    with self.assertRaises(GateError):
                        validate_record(self.root, self.base)
                    target.write_bytes(original)
                    git(self.root, 'update-index', undo, path)
        validate_record(self.root, self.base)

    def test_git_flags_cannot_hide_module_mode_or_symlink(self):
        after = self.module_baseline()
        self.module_record(after)
        for flag, undo in (('--assume-unchanged', '--no-assume-unchanged'), ('--skip-worktree', '--no-skip-worktree')):
            for path in ('go.mod', 'go.sum'):
                with self.subTest(flag=flag, path=path):
                    target = self.root / path
                    original = target.read_bytes()
                    git(self.root, 'update-index', flag, path)
                    target.chmod(0o755)
                    with self.assertRaises(GateError):
                        validate_record(self.root, self.base)
                    target.chmod(0o644)
                    target.unlink()
                    target.symlink_to('formal/models.json')
                    with self.assertRaises(GateError):
                        validate_record(self.root, self.base)
                    target.unlink()
                    with self.assertRaises(GateError):
                        validate_record(self.root, self.base)
                    target.write_bytes(original)
                    git(self.root, 'update-index', undo, path)
        validate_record(self.root, self.base)

    def test_module_inputs_are_rechecked_after_classification_with_git_flags(self):
        after = self.module_baseline()
        self.module_record(after)
        real = formal_applicability.module_metadata_change
        for flag, undo in (('--assume-unchanged', '--no-assume-unchanged'), ('--skip-worktree', '--no-skip-worktree')):
            for path in ('go.mod', 'go.sum'):
                target = self.root / path
                original = target.read_bytes()
                def tamper(*args):
                    result = real(*args)
                    target.write_bytes(original + b'\nchanged after classification\n')
                    return result
                with self.subTest(flag=flag, path=path):
                    git(self.root, 'update-index', flag, path)
                    with patch('formal_applicability.module_metadata_change', side_effect=tamper):
                        with self.assertRaises(GateError):
                            validate_record(self.root, self.base)
                    target.write_bytes(original)
                    git(self.root, 'update-index', undo, path)
        validate_record(self.root, self.base)


if __name__ == '__main__':
    unittest.main()
