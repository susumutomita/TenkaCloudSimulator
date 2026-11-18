"""Real native service reads must commit once across menu/history navigation."""
import json
import os
import shutil
from pathlib import Path
from playwright.sync_api import expect, sync_playwright
from check import running, REPORTS


def run():
    requests, responses, failed = [], [], []
    with running(authenticated=True) as (endpoint, token), sync_playwright() as p:
        browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
        try:
            context = browser.new_context()
            page = context.new_page()
            watched = {'AmazonSQS.ListQueues', 'Logs_20140328.DescribeLogGroups'}
            def operation(request):
                return request.headers.get('x-amz-target', '')
            page.on('request', lambda request: requests.append(operation(request)) if operation(request) in watched else None)
            page.on('response', lambda response: responses.append((operation(response.request), response.status)) if operation(response.request) in watched else None)
            page.on('requestfailed', lambda request: failed.append(operation(request)) if operation(request) in watched else None)
            assert context.request.post(endpoint+'/_simulator/login', form={'token': token}, max_redirects=0).status == 303
            page.goto(endpoint+'/_simulator/console/')
            def choose(name, target):
                page.get_by_role('button', name='Services', exact=True).click()
                entry = page.get_by_role('menuitem', name=name, exact=True)
                entry.focus()
                with page.expect_response(lambda response: operation(response.request) == target) as received:
                    entry.press('Enter')
                assert received.value.status == 200
                expect(page.get_by_role('button', name='Services', exact=True)).to_be_focused()
            choose('SQS', 'AmazonSQS.ListQueues')
            expect(page.get_by_role('heading', name='Queues (0)', exact=True)).to_be_visible()
            assert len(requests) == len(responses) == 1 and not failed
            choose('CloudWatch Logs', 'Logs_20140328.DescribeLogGroups')
            expect(page.get_by_role('table', name='Log groups', exact=True)).to_be_visible()
            assert len(requests) == len(responses) == 2 and not failed
            with page.expect_response(lambda response: operation(response.request) == 'AmazonSQS.ListQueues') as back:
                page.go_back()
            assert back.value.status == 200
            expect(page.get_by_role('heading', name='Queues (0)', exact=True)).to_be_visible()
            assert len(requests) == len(responses) == 3 and not failed
            with page.expect_response(lambda response: operation(response.request) == 'Logs_20140328.DescribeLogGroups') as forward:
                page.go_forward()
            assert forward.value.status == 200
            expect(page.get_by_role('table', name='Log groups', exact=True)).to_be_visible()
            assert len(requests) == len(responses) == 4 and not failed
            # Re-selecting the current service refreshes once without adding a
            # duplicate history entry. Back still returns to the previous SQS.
            choose('CloudWatch Logs', 'Logs_20140328.DescribeLogGroups')
            assert len(requests) == len(responses) == 5 and not failed
            with page.expect_response(lambda response: operation(response.request) == 'AmazonSQS.ListQueues') as same_back:
                page.go_back()
            assert same_back.value.status == 200
            expect(page.get_by_role('heading', name='Queues (0)', exact=True)).to_be_visible()
            assert len(requests) == len(responses) == 6 and not failed
            context.close()
        finally:
            browser.close()
    report = {'requestOperations': requests, 'responseStatuses': [status for _, status in responses], 'abortedReads': failed, 'menuKeyboard': True, 'backForward': True, 'sameServiceNoDuplicateHistory': True}
    (REPORTS/'navigation-single-commit.json').write_text(json.dumps(report, indent=2)+'\n')
    print(json.dumps(report))


if __name__ == '__main__':
    run()
