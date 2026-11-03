"""Check that missing, incomplete, or repeated coverage cannot inflate the gate."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('coverage_gate', Path(__file__).with_name('check-coverage.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class CoverageGate(unittest.TestCase):
    def test_weighted_statements_and_duplicate_blocks(self):
        profile = 'mode: atomic\na.go:1.1,2.1 9 0\na.go:1.1,2.1 9 1\na.go:3.1,4.1 1 0\n'
        self.assertEqual(module.statement_coverage(profile), 0.90)
        self.assertLess(module.statement_coverage(profile.replace('9 1', '9 0')), 0.90)

    def test_invalid_profiles_fail_closed(self):
        for profile in ['', 'mode: atomic\n', 'mode: ignored\n', 'mode: count\na 1 -1\n', 'mode: count\na 1 1\na 2 1\n']:
            with self.subTest(profile=profile), self.assertRaises(ValueError):
                module.statement_coverage(profile)


if __name__ == '__main__':
    unittest.main()
