"""Wait for responsive layout completion without tolerating actual overflow."""
import json
import uuid
from playwright.sync_api import TimeoutError as PlaywrightTimeoutError


def assert_reflow(page, timeout_ms=3000):
    # Cloudscape observes resize asynchronously. A viewport change alone does
    # not mean its React/ResizeObserver layout has finished. Keep the exact
    # width condition and require it to remain stable for 100 ms.
    page.evaluate('document.fonts.ready')
    try:
        page.wait_for_function('''token => {
          const width=document.documentElement.scrollWidth, viewport=innerWidth;
          if(width>viewport){window.__tcsimReflow=undefined;return false;}
          const old=window.__tcsimReflow;
          if(!old||old.token!==token||old.width!==width||old.viewport!==viewport){
            window.__tcsimReflow={token,width,viewport,since:performance.now()};return false;
          }
          return performance.now()-old.since>=100;
        }''', arg=uuid.uuid4().hex, timeout=timeout_ms)
    except PlaywrightTimeoutError as error:
        # Geometry only: never log input values, text, cookies or access tokens.
        geometry=page.evaluate('''() => ({width:document.documentElement.scrollWidth,viewport:innerWidth,
          offenders:[...document.querySelectorAll('body *')].map(n=>({tag:n.tagName,left:n.getBoundingClientRect().left,right:n.getBoundingClientRect().right}))
          .filter(n=>n.right>innerWidth||n.left<0).slice(0,8)})''')
        raise AssertionError('Responsive overflow persisted: '+json.dumps(geometry)) from error
