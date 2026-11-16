"""Exercise the actual coverage CLI: missing or low reports must fail CI."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('check-coverage.py').resolve()


class CoverageGate(unittest.TestCase):
    def run_gate(self, profile, xml):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source, report, summary = root/'coverage.out', root/'coverage.xml', root/'job-summary.md'
            if profile is not None:
                source.write_text(profile)
            if xml is not None:
                report.write_text(xml)
            result = subprocess.run([sys.executable, str(SCRIPT), str(source), str(report)], env={**os.environ, 'GITHUB_STEP_SUMMARY': str(summary)}, capture_output=True, text=True)
            return result.returncode, summary.read_text() if summary.exists() else ''

    def test_valid_reports_publish_visible_summary(self):
        code, summary = self.run_gate('mode: atomic\nf.go:1.1,2.1 9 1\nf.go:3.1,4.1 1 0\n', '<coverage lines-covered="90" lines-valid="100"/>')
        self.assertEqual(code, 0)
        self.assertIn('**PASS**', summary)
        self.assertIn('90.00%', summary)

    def test_each_floor_independently_fails(self):
        for statements, lines in [(89, 100), (100, 89)]:
            with self.subTest(statements=statements, lines=lines):
                profile = f'mode: count\nf.go:1.1,2.1 {statements} 1\nf.go:3.1,4.1 {100-statements} 0\n'
                code, summary = self.run_gate(profile, f'<coverage lines-covered="{lines}" lines-valid="100"/>')
                self.assertNotEqual(code, 0)
                self.assertIn('**FAIL**', summary)

    def test_duplicate_blocks_do_not_inflate_coverage(self):
        profile = 'mode: count\nf.go:1.1,2.1 89 1\nf.go:1.1,2.1 89 1\nf.go:3.1,4.1 11 0\n'
        code, _ = self.run_gate(profile, '<coverage lines-covered="100" lines-valid="100"/>')
        self.assertNotEqual(code, 0)

    def test_missing_empty_and_malformed_reports_fail(self):
        valid_profile = 'mode: atomic\nf.go:1.1,2.1 1 1\n'
        valid_xml = '<coverage lines-covered="1" lines-valid="1"/>'
        for profile, xml in [(None, valid_xml), ('', valid_xml), ('mode: atomic\n', valid_xml), ('invalid', valid_xml), (valid_profile, None), (valid_profile, ''), (valid_profile, '<coverage lines-covered="0" lines-valid="0"/>'), (valid_profile, '<coverage lines-covered="2" lines-valid="1"/>')]:
            with self.subTest(profile=profile, xml=xml):
                code, _ = self.run_gate(profile, xml)
                self.assertNotEqual(code, 0)


HEAD = 'a' * 40
EXPECTED = ['example/a', 'example/b', 'example/c']
VALID_SHARDS = [('first', HEAD, ['example/a'], 'mode: atomic\na.go:1.1,2.1 3 1\nb.go:1.1,2.1 2 0\n'),
                ('second', HEAD, ['example/b', 'example/c'], 'mode: atomic\na.go:1.1,2.1 3 0\nb.go:1.1,2.1 2 4\n')]


class ShardMerge(unittest.TestCase):
    def run_merge(self, shards, expected=EXPECTED):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root/'expected.txt').write_text('\n'.join(expected) + '\n')
            manifests = []
            for name, head, packages, profile in shards:
                if profile is not None:
                    (root/f'coverage-{name}.out').write_text(profile)
                (root/f'{name}.json').write_text(json.dumps({'head': head, 'packages': packages, 'profile': f'coverage-{name}.out'}))
                manifests.append(str(root/f'{name}.json'))
            result = subprocess.run([sys.executable, str(SCRIPT), 'merge', str(root/'merged.out'), str(root/'expected.txt'), HEAD, *manifests], capture_output=True, text=True)
            merged = (root/'merged.out').read_text() if (root/'merged.out').exists() else None
            return result.returncode, merged

    def test_shards_covering_each_package_once_merge_into_one_profile(self):
        code, merged = self.run_merge(VALID_SHARDS)
        self.assertEqual(code, 0)
        self.assertEqual(merged, 'mode: atomic\na.go:1.1,2.1 3 1\nb.go:1.1,2.1 2 0\na.go:1.1,2.1 3 0\nb.go:1.1,2.1 2 4\n')

    def test_merged_shard_blocks_count_as_hit_when_any_shard_hit_them(self):
        code, merged = self.run_merge(VALID_SHARDS)
        self.assertEqual(code, 0)
        with tempfile.NamedTemporaryFile('w', suffix='.out') as profile:
            profile.write(merged)
            profile.flush()
            result = subprocess.run([sys.executable, str(SCRIPT), profile.name], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('5/5 (100.00%', result.stdout)

    def test_missing_duplicate_foreign_or_mismatched_shards_fail(self):
        first, second = VALID_SHARDS
        cases = {
            'missing package': [first, (second[0], HEAD, ['example/b'], second[3])],
            'package in two shards': [first, (second[0], HEAD, ['example/a', 'example/b', 'example/c'], second[3])],
            'unexpected package': [first, (second[0], HEAD, ['example/b', 'example/c', 'example/d'], second[3])],
            'other head': [first, (second[0], 'b' * 40, second[2], second[3])],
            'mode mismatch': [first, (second[0], HEAD, second[2], 'mode: count\na.go:1.1,2.1 3 0\n')],
            'missing profile': [first, (second[0], HEAD, second[2], None)],
            'empty package list': [first, second, ('third', HEAD, [], 'mode: atomic\n')],
            'no manifests': [],
        }
        for name, shards in cases.items():
            with self.subTest(name):
                code, merged = self.run_merge(shards)
                self.assertNotEqual(code, 0)
                self.assertIsNone(merged)


if __name__ == '__main__':
    unittest.main()
