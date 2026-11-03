"""Positive resize synchronization and mandatory overflow-detector regression."""
import os
import shutil
import unittest
from playwright.sync_api import sync_playwright
from reflow import assert_reflow


class ReflowGate(unittest.TestCase):
    def test_waits_for_responsive_observer_then_enforces_exact_width(self):
        with sync_playwright() as p:
            executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
            browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox'])
            page=browser.new_page(viewport={'width':390,'height':844})
            page.set_content('<style>body{margin:0}</style><main style="width:1000px">Dummy layout</main>')
            self.assertFalse(page.evaluate('document.documentElement.scrollWidth<=innerWidth'))
            page.evaluate("setTimeout(()=>document.querySelector('main').style.width='100%',150)")
            assert_reflow(page)
            self.assertTrue(page.evaluate('document.documentElement.scrollWidth<=innerWidth'))
            # A permanently oversized layout still fails; no retry, skipped
            # assertion, width tolerance or error suppression makes it pass.
            page.evaluate("document.querySelector('main').style.width='1000px'")
            with self.assertRaisesRegex(AssertionError,'Responsive overflow persisted'):
                assert_reflow(page,timeout_ms=250)
            browser.close()


if __name__=='__main__':
    unittest.main(verbosity=2)
