"""Exercise the actual coverage CLI: missing or low reports must fail CI."""
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


if __name__ == '__main__':
    unittest.main()
