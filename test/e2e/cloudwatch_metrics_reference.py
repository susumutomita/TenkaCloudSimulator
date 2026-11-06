"""Actual served Scalar Query examples against two owned native CLI databases.

SOURCE ONLY until an explicit browser lease and a fresh combined embedded CLI.
No request interception/fulfilment, editor-state mutation, fake capability or
substitute fetch counts as Scalar Send. All fixture values are public dummy data.
"""
from contextlib import ExitStack
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import tempfile
from urllib.parse import parse_qs, urlsplit
from xml.etree import ElementTree as ET

MEDIA = 'application/x-www-form-urlencoded'
NS = '{http://monitoring.amazonaws.com/doc/2010-08-01/}'
EXAMPLES = ('CloudWatchPutMetricData', 'CloudWatchListMetrics', 'CloudWatchGetMetricStatistics')
STATS = {'SampleCount': 1, 'Sum': 7, 'Minimum': 7, 'Maximum': 7, 'Average': 7}


def exact_named_button(page, labels):
    """Union exact accessible names without regexp-delimiter serialization."""
    assert not isinstance(labels, (str, bytes)), 'Expected reviewed button-name inventory'
    labels = tuple(labels)
    assert labels and all(isinstance(label, str) and label for label in labels)
    assert len(set(labels)) == len(labels), 'Ambiguous reviewed button-name inventory'
    button = page.get_by_role('button', name=labels[0], exact=True)
    for label in labels[1:]:
        button = button.or_(page.get_by_role('button', name=label, exact=True))
    return button


def digest(data):
    return hashlib.sha256(data).hexdigest()


def xml_response(response, action, status=200, code=None):
    assert response.status == status, 'Unexpected real Scalar response status'
    assert response.headers.get('content-type', '').startswith('text/xml')
    root = ET.fromstring(response.body())
    if status == 200:
        assert root.tag == NS + action + 'Response'
        assert root.findtext(NS + 'ResponseMetadata/' + NS + 'RequestId')
    else:
        assert root.tag == NS + 'ErrorResponse'
        assert root.findtext(NS + 'Error/' + NS + 'Code') == code
        assert root.findtext(NS + 'Error/' + NS + 'Type') == 'Sender'
        assert root.findtext(NS + 'RequestId')
    return root


def typed_row(root):
    members = root.findall(NS + 'GetMetricStatisticsResult/' + NS + 'Datapoints/' + NS + 'member')
    assert len(members) == 1, 'Expected one actual Count/time datapoint'
    row = members[0]
    assert row.findtext(NS + 'Unit') == 'Count'
    assert {s: float(row.findtext(NS + s)) for s in STATS} == STATS
    assert root.findtext(NS + 'GetMetricStatisticsResult/' + NS + 'Label') == 'Requests'
    return datetime.fromisoformat(row.findtext(NS + 'Timestamp').replace('Z', '+00:00'))


def run(binary, reports):
    # Imports are deliberately lazy: source inspection creates no clients.
    from playwright.sync_api import sync_playwright, expect
    import cloudwatch_metrics as native
    from metrics_openapi import contract
    from metrics_openapi import preserved_metrics_reference as preserved_reference

    binary, reports = Path(binary).resolve(), Path(reports).resolve()
    reports.mkdir(parents=True, exist_ok=False)
    spec_source = Path('internal/server/apidocs/openapi.json').read_bytes()
    expected_spec = json.loads(spec_source)
    preserved_reference(expected_spec)
    examples = contract()['examples']
    axe_path = Path(os.environ['AXE_SCRIPT'])
    result = {'status': 'RUNNING', 'scope': 'Actual embedded Scalar Send, not static template validation',
              'binarySHA256': digest(binary.read_bytes()), 'specSHA256': digest(spec_source),
              'helperSHA256': digest(Path(__file__).read_bytes()), 'axeSHA256': digest(axe_path.read_bytes()),
              'sends': [], 'audits': [], 'cleanup': False, 'notClaimed': ['Full AWS compatibility', 'Real AWS authentication', 'Whole formal/CI acceptance']}
    phase = 'initialization'
    try:
        with tempfile.TemporaryDirectory(prefix='metric-scalar-', dir='/tmp') as temporary, sync_playwright() as pw, ExitStack() as stack:
            browser = pw.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE', '/usr/bin/chromium'), args=['--no-sandbox'])
            stack.callback(browser.close)
            for width, height in ((1440, 1000), (390, 844)):
                # Fresh databases ensure the literal one-Send/one-sample oracle.
                with ExitStack() as owned:
                    folder = Path(temporary) / str(width)
                    a = native.Team(owned, binary, folder / 'a').start()
                    b = native.Team(owned, binary, folder / 'b').start()
                    sdk, _ = native.client(owned, a)
                    peer, _ = native.client(owned, b)
                    dims = [{'Name': 'Stage', 'Value': 'local'}]
                    peer_seed_time = datetime.now(timezone.utc)
                    peer.put_metric_data(Namespace='Simulator/Reference', MetricData=[{'MetricName': 'Requests', 'Dimensions': dims, 'Value': 101, 'Unit': 'Count', 'Timestamp': peer_seed_time}])
                    iam, _ = native.client(owned, a, service='iam')
                    role = 'metrics-reference-reader'
                    iam.create_role(RoleName=role, AssumeRolePolicyDocument=json.dumps({'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Principal': {'Service': 'lambda.amazonaws.com'}, 'Action': 'sts:AssumeRole'}]}))
                    iam.put_role_policy(RoleName=role, PolicyName='read-only', PolicyDocument=json.dumps({'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': ['cloudwatch:ListMetrics', 'cloudwatch:GetMetricStatistics'], 'Resource': '*'}]}))
                    reader = native.control(a, '/_simulator/v1/access', {'Principal': role}, 'POST')['AccessToken']
                    context = browser.new_context(viewport={'width': width, 'height': height})
                    owned.callback(context.close)
                    outside, errors, transport = [], [], []
                    origin = urlsplit(a.endpoint)
                    def route_guard(route):
                        actual = urlsplit(route.request.url)
                        if (actual.scheme, actual.netloc) == (origin.scheme, origin.netloc):
                            route.continue_()
                        else:
                            outside.append({'scheme': actual.scheme})
                            route.abort()
                    context.route('**/*', route_guard)
                    page = context.new_page()
                    page.on('pageerror', lambda error: errors.append(type(error).__name__))
                    def observe_response(response):
                        request = response.request
                        if request.method == 'POST' and response.url == a.endpoint + '/':
                            transport.append(response.status)
                    page.on('response', observe_response)
                    def capture_before_cleanup(exc_type, exception, traceback):
                        # ExitStack invokes this before context.close. Preserve
                        # the primary exception; diagnostics never suppress it.
                        if exc_type is None or result.get('firstFailureDiagnostic'):
                            return False
                        diagnostic = {'width': width, 'phase': phase,
                                      'primaryType': exc_type.__name__,
                                      'privacy': 'No input values, HTTP headers, cookies, tokens or DOM/HTML dump.'}
                        result['firstFailureDiagnostic'] = diagnostic
                        form = expected_spec['paths']['/']['post']['requestBody']['content'][MEDIA]
                        known_names = {example.get('summary', name) for name, example in form['examples'].items()}
                        known_names |= {'Selected Content Type: ' + media for media in expected_spec['paths']['/']['post']['requestBody']['content']}
                        known_names |= {'Test Request (post /)', 'Close Client', 'Send Request'}
                        fields = {key for example in examples.values() for key in example['value']}
                        fields |= {'X-Simulator-Service', 'X-Simulator-Region', 'X-Amz-Target', 'Authorization', 'X-Simulator-Token'}
                        known_names |= {name + ' Value' for name in fields}
                        known_names |= {'Include ' + name + ' in request' for name in fields}
                        try:
                            # Diagnostic only: inspect the actual API Client,
                            # including preset buttons omitted by the earlier
                            # editor-only inventory. Never inspect credential
                            # row values. Only enum membership is exported.
                            client = page.get_by_role('dialog', name='API Client', exact=True)
                            enum_values = {}
                            for parameter in expected_spec['paths']['/']['post']['parameters']:
                                key = parameter.get('name', '').casefold()
                                if key in ('x-simulator-service', 'x-simulator-region'):
                                    enum_values[key] = parameter['schema']['enum']
                            enum_values['content-type'] = list(expected_spec['paths']['/']['post']['requestBody']['content'])
                            approved = examples[EXAMPLES[0]]['value']
                            complete = {'clientCount': client.count()}
                            if complete['clientCount'] == 1:
                                complete.update(client.evaluate("""(client, config) => {
                                    const enums=config.enums;
                                    const knownKeys=new Set(config.keys.map(k=>k.toLowerCase()));
                                    const shape=n=>({tag:n.tagName.toLowerCase(),role:n.getAttribute('role')||'',editable:n.getAttribute('contenteditable')||'',label:n.getAttribute('aria-label')||'',selected:n.getAttribute('aria-selected'),disabled:!!n.disabled||n.getAttribute('aria-disabled')==='true'});
                                    const rows=[];
                                    for(const [key,allowed] of Object.entries(enums)) {
                                        const matches=Array.from(client.querySelectorAll('tr[id]')).filter(n=>n.id.toLowerCase()===key);
                                        rows.push({key,count:matches.length,controls:matches.length===1?Array.from(matches[0].querySelectorAll('button,input,select,textarea,[contenteditable],[role="combobox"],[role="checkbox"],[role="tab"]')).slice(0,16).map(n=>{
                                            const item=shape(n);
                                            // Text is read ONLY in these three
                                            // nonsecret schema-enum rows.
                                            const text=(n.tagName==='BUTTON'||n.getAttribute('role')==='option')?(n.textContent||'').trim():'';
                                            return {...item,checked:n.type==='checkbox'?n.checked:null,enumMembership:allowed.filter(v=>text===v||n.getAttribute('aria-label')===v)};
                                        }):[]});
                                    }
                                    const controls=Array.from(client.querySelectorAll('button,[role="tab"],[role="option"],select')).slice(0,160).map(n=>{
                                        const item=shape(n);const row=n.closest('tr[id]');
                                        // Never read button text in credential
                                        // rows or arbitrary client controls.
                                        return {...item,knownKey:row&&knownKeys.has(row.id.toLowerCase())?row.id.toLowerCase():null};
                                    });
                                    // Scalar's body code editor is distinct from
                                    // RequestTableRow CodeInputLite. Read only a
                                    // unique unlabelled CodeMirror body editor,
                                    // never any header/auth/value editor.
                                    const editors=Array.from(client.querySelectorAll('.cm-content[role="textbox"][contenteditable="true"]')).filter(n=>!n.closest('tr[id]')&&!n.getAttribute('aria-label'));
                                    let body={editorCount:editors.length,classification:'unobserved',approvedExampleEqual:false,approvedKeys:[],unknownKeyCount:0};
                                    if(editors.length===1) {
                                        const raw=editors[0].textContent||'';
                                        body.withinDiagnosticBound=raw.length<=262144;
                                        if(body.withinDiagnosticBound) {
                                            let value=null;
                                            try {value=JSON.parse(raw);body.classification='json';} catch (_) {
                                                try {const q=new URLSearchParams(raw);if(raw.includes('=')){value=Object.fromEntries(q);body.classification='urlencoded';}} catch (_) {}
                                            }
                                            if(value&&typeof value==='object'&&!Array.isArray(value)) {
                                                const keys=Object.keys(value), expected=Object.keys(config.approved);
                                                body.approvedKeys=keys.filter(k=>expected.includes(k)).sort();
                                                body.unknownKeyCount=keys.filter(k=>!expected.includes(k)).length;
                                                body.approvedExampleEqual=keys.length===expected.length&&expected.every(k=>Object.hasOwn(value,k)&&String(value[k])===String(config.approved[k]));
                                            }
                                        }
                                    }
                                    return {rows,controls,body,maxControls:160,maxRowControls:16};
                                }""", {'enums': enum_values, 'keys': sorted(fields), 'approved': approved}))
                            def redact_label(value):
                                if value.strip() in allowed_complete:
                                    return {'staticName': value.strip()}
                                return {'unknownNameSHA256': digest(value.encode()), 'length': len(value)}
                            allowed_complete = {'Key', 'Value', 'Headers', 'Request Body', 'Body', 'Form', 'Raw', 'JSON', 'Form Data', 'Form URL Encoded', 'Text', 'Binary', 'None'} | known_names
                            allowed_complete |= {value for values in enum_values.values() for value in values}
                            for row in complete.get('rows', []):
                                for control in row['controls']:
                                    control['label'] = redact_label(control['label'])
                            for control in complete.get('controls', []):
                                control['label'] = redact_label(control['label'])
                            complete['privacy'] = 'Only nonsecret enum membership; approved body equality/key names only. No raw body/header/credential values, cookies, tokens or DOM.'
                            path = reports / 'first-failure-complete-client-structure.json'
                            path.write_text(json.dumps(complete, indent=2) + '\n')
                            diagnostic['completeClientStructure'] = path.name
                        except Exception as error:
                            diagnostic['completeClientStructureErrorType'] = type(error).__name__
                        try:
                            # Only the OPEN API client, not the documentation
                            # sidebar. RequestTableRow sets id=data.name; known
                            # key presence can be observed without reading an
                            # editor's value or contenteditable text.
                            client = page.get_by_role('dialog', name='API Client', exact=True)
                            client_count = client.count()
                            known_keys = sorted(fields)
                            generic_names = {'Key', 'Value', 'row Key', 'row Value'}
                            for prefix in ('Header', 'Headers', 'Body', 'Request Body', 'Query', 'Query Parameters', 'Parameter'):
                                generic_names |= {prefix + ' Key', prefix + ' Value'}
                            allowed_names = known_names | generic_names
                            controls = []
                            key_rows = []
                            if client_count == 1:
                                observed = client.evaluate("""(client, known) => {
                                    const keys=new Set(known.map(k=>k.toLowerCase()));
                                    const keyOf=node=>{for(let p=node,depth=0;p && p!==client && depth<7;p=p.parentElement,depth++){const key=(p.getAttribute('id')||'').toLowerCase();if(keys.has(key))return {key,tag:p.tagName.toLowerCase(),role:p.getAttribute('role')||''};}return null;};
                                    const staticRefs=node=>(node.getAttribute('aria-labelledby')||'').split(/\\s+/).filter(Boolean).slice(0,4).flatMap(id=>{const label=document.getElementById(id);if(!label || !label.matches('label,th,[role="columnheader"]') || label.querySelector('input,textarea,[contenteditable],pre,code'))return [];return [(label.textContent||'').trim()];});
                                    return {
                                        rows:Array.from(client.querySelectorAll('[id]')).filter(n=>keys.has((n.getAttribute('id')||'').toLowerCase())).slice(0,80).map(n=>({key:n.getAttribute('id').toLowerCase(),tag:n.tagName.toLowerCase(),role:n.getAttribute('role')||''})),
                                        controls:Array.from(client.querySelectorAll('input,select,textarea,[contenteditable],[role="combobox"],[role="textbox"],[role="checkbox"]')).slice(0,160).map(n=>({tag:n.tagName.toLowerCase(),role:n.getAttribute('role')||'',contenteditable:n.getAttribute('contenteditable')||'',ariaLabel:n.getAttribute('aria-label')||'',labelledby:staticRefs(n),knownRow:keyOf(n)}))
                                    };
                                }""", known_keys)
                                def safe_name(name):
                                    trimmed = name.strip()
                                    if trimmed in allowed_names and len(trimmed) <= 200:
                                        return {'staticName': trimmed}
                                    return {'unknownNameSHA256': digest(name.encode()), 'length': len(name)}
                                for control in observed['controls']:
                                    controls.append({'tag': control['tag'], 'role': control['role'],
                                        'contenteditable': control['contenteditable'],
                                        'ariaLabel': safe_name(control['ariaLabel']),
                                        'labelledby': [safe_name(name) for name in control['labelledby']],
                                        'knownRow': control['knownRow']})
                                key_rows = observed['rows']
                            expected_counts = {}
                            for key in known_keys:
                                folded = key.casefold()
                                expected_counts[folded] = {'knownKeyRows': sum(row['key'] == folded for row in key_rows),
                                    'controlsInKnownRow': sum(control['knownRow'] is not None and control['knownRow']['key'] == folded for control in controls),
                                    'exactExpectedValueNames': sum(control['ariaLabel'].get('staticName') == key + ' Value' for control in controls)}
                            path = reports / 'first-failure-client-control-structure.json'
                            path.write_text(json.dumps({'phase': phase, 'width': width,
                                'clientRole': 'dialog', 'clientName': 'API Client', 'clientCount': client_count,
                                'knownKeyRows': key_rows, 'expectedKeyPresence': expected_counts,
                                'maxControls': 160, 'maxKnownRows': 80, 'controls': controls,
                                'privacy': 'Known casefolded row IDs only; no value/contenteditable text/header/cookie/token/fullDOM reads. Unknown declared names are SHA256+length.'}, indent=2) + '\n')
                            diagnostic['clientControlStructure'] = path.name
                        except Exception as error:
                            diagnostic['clientStructureCaptureErrorType'] = type(error).__name__
                        try:
                            # Diagnostic-only structural observation of the five
                            # known MIME controls. This does not choose one or
                            # relax the unchanged acceptance count1 assertion.
                            media_values = tuple(expected_spec['paths']['/']['post']['requestBody']['content'])
                            names = ['Selected Content Type: ' + value for value in media_values]
                            matches = page.get_by_role('button', name=names[0], exact=True)
                            for name in names[1:]:
                                matches = matches.or_(page.get_by_role('button', name=name, exact=True))
                            count = matches.count()
                            allowed = ['Request Body', 'Response', 'Responses', *media_values, *names]
                            ancestors = matches.evaluate_all("""(buttons, allowed) => buttons.slice(0,5).map(button => {
                                const chain=[];
                                for(let node=button,depth=0;node && depth<9;node=node.parentElement,depth++){
                                    const attr=name=>(node.getAttribute(name)||'').slice(0,192);
                                    const label=node.getAttribute('aria-label')||'';
                                    const headings=Array.from(node.querySelectorAll('h1,h2,h3,h4,h5,h6,[role="heading"]')).slice(0,16).map(h=>(h.textContent||'').trim()).filter(text=>allowed.includes(text));
                                    chain.push({depth,tag:node.tagName.toLowerCase(),role:attr('role'),id:attr('id'),testId:attr('data-testid'),className:attr('class'),staticLabel:allowed.includes(label)?label:'',staticHeadings:[...new Set(headings)]});
                                }
                                return {ancestors:chain};
                            })""", allowed)
                            path = reports / 'first-failure-matched-media-ancestors.json'
                            path.write_text(json.dumps({'phase': phase, 'width': width, 'matchedCount': count,
                                'capturedControls': len(ancestors), 'maxControls': 5, 'maxAncestorsPerControl': 9,
                                'maxAttributeCharacters': 192, 'controls': ancestors,
                                'qualification': 'Static structural attributes/whitelisted headings only; no selection or acceptance inference.'}, indent=2) + '\n')
                            diagnostic['matchedMediaAncestors'] = path.name
                        except Exception as error:
                            diagnostic['ancestorCaptureErrorType'] = type(error).__name__
                        try:
                            # Read only roles/declared names and button labels;
                            # never read value, contenteditable text or headers.
                            shapes = page.evaluate("""() => Array.from(document.querySelectorAll('button,input,select,textarea,[role],[contenteditable]')).slice(0,160).map(e => ({tag:e.tagName.toLowerCase(), role:e.getAttribute('role') || '', type:e.getAttribute('type') || '', name:e.getAttribute('aria-label') || (e.tagName==='BUTTON' ? e.innerText : '')}))""")
                            safe = []
                            for shape in shapes:
                                name = shape['name'].strip()
                                safe.append({'tag': shape['tag'], 'role': shape['role'], 'type': shape['type'],
                                             'name': name if name in known_names and len(name) <= 200 else '<redacted-unreviewed-name>'})
                            path = reports / 'first-failure-control-shapes.json'
                            path.write_text(json.dumps({'phase': phase, 'width': width, 'controls': safe}, indent=2) + '\n')
                            diagnostic['controlShapes'] = path.name
                        except Exception as error:
                            diagnostic['shapeCaptureErrorType'] = type(error).__name__
                        try:
                            path = reports / 'first-failure-masked.png'
                            page.screenshot(path=str(path), full_page=False, timeout=5000,
                                mask=[page.locator('input,textarea,select,[contenteditable],table,pre,code,.cm-editor,[data-testid*="auth"],[data-testid*="header"]')])
                            diagnostic['maskedScreenshot'] = path.name
                        except Exception as error:
                            diagnostic['screenshotErrorType'] = type(error).__name__
                        return False

                    owned.push(capture_before_cleanup)
                    phase = 'served-spec-' + str(width)
                    assert context.request.post(a.endpoint + '/_simulator/login', form={'token': a.token}, max_redirects=0).status == 303
                    served = context.request.get(a.endpoint + '/_simulator/docs/openapi.json')
                    assert served.status == 200 and served.json() == expected_spec
                    preserved_reference(served.json())
                    # The anonymous Access refusal precedes native parsing.
                    anonymous = browser.new_context()
                    try:
                        denied = anonymous.request.post(a.endpoint + '/', data='Action=ListMetrics&Version=2010-08-01', headers={'Content-Type': MEDIA, 'X-Simulator-Service': 'monitoring'})
                        assert denied.status == 401
                    finally:
                        anonymous.close()
                    peer_frame = native.frame(b.db)
                    local_frame = native.frame(a.db)

                    def exact_button_union(names, scope):
                        # Preserve the reviewed inventory guard and use the
                        # same exact-name constructor covered by pure fixtures.
                        assert names and len(names) == len(set(names))
                        return exact_named_button(scope, names)

                    def api_client():
                        client = page.get_by_role('dialog', name='API Client', exact=True)
                        expect(client).to_have_count(1)
                        return client

                    def client_body():
                        # Pinned API-client CollapsibleSection renders a
                        # heading and its panel within the same section.
                        heading = api_client().get_by_role('heading', name='Request Body', exact=True)
                        expect(heading).to_have_count(1)
                        body = heading.locator('xpath=ancestor::section[1]')
                        expect(body).to_have_count(1)
                        return body

                    def header_preset(name, value):
                        parameter = next(p for p in expected_spec['paths']['/']['post']['parameters'] if p.get('name') == name)
                        allowed = parameter['schema']['enum']
                        assert value in allowed
                        row = api_client().locator('tr[id=' + json.dumps(name) + ' i]')
                        expect(row).to_have_count(1)
                        include = row.get_by_role('checkbox', name='Include ' + name + ' in request', exact=True)
                        expect(include).to_have_count(1)
                        include.check()
                        # Actual a3: these are preset buttons, not Value editors.
                        preset = exact_button_union(allowed, row)
                        expect(preset).to_have_count(1)
                        if preset.inner_text().strip() != value:
                            preset.click()
                            option = page.get_by_role('menuitem', name=value, exact=True)
                            expect(option).to_have_count(1)
                            option.click()
                        expect(row.get_by_role('button', name=value, exact=True)).to_have_count(1)

                    def field(name, value):
                        body = client_body()
                        row = body.locator('tr[id=' + json.dumps(name) + ']')
                        assert row.count() <= 1
                        if not row.count():
                            # RequestBodyForm exposes exactly one blank row.
                            # CodeInputLite commits a typed key on ordinary blur.
                            blank = body.locator('tr[id=""]')
                            expect(blank).to_have_count(1)
                            key = blank.get_by_role('combobox', name='Key', exact=True)
                            expect(key).to_have_count(1)
                            key.fill(name)
                            key.press('Tab')
                        expect(row).to_have_count(1)
                        include = row.get_by_role('checkbox', name='Include ' + name + ' in request', exact=True)
                        assert include.count() <= 1
                        if include.count():
                            include.check()
                        cells = row.get_by_role('cell')
                        expect(cells).to_have_count(3)
                        # RequestTableRow's third cell is the value cell;
                        # key and value editors share generic accessible names.
                        value_cell = cells.nth(2)
                        control = value_cell.get_by_role('combobox', name='Value', exact=True)
                        assert control.count() <= 1
                        if control.count():
                            expect(control).to_have_count(1)
                            expect(control).to_be_visible()
                            control.fill(str(value))
                            control.press('Tab')
                        else:
                            # CodeInputLite renders schema enum/example values
                            # through DataTableInputSelect. Admit only values
                            # from the frozen form schema/examples, never an
                            # arbitrary button or positional first match.
                            allowed = {'Select a value', str(value)}
                            def collect(node):
                                if isinstance(node, dict):
                                    for enum_value in node.get('enum', []):
                                        if isinstance(enum_value, (str, int, float, bool)):
                                            allowed.add(str(enum_value))
                                    for child in node.values():
                                        collect(child)
                                elif isinstance(node, list):
                                    for child in node:
                                        collect(child)
                            form = expected_spec['paths']['/']['post']['requestBody']['content'][MEDIA]
                            collect(form['schema'])
                            for example in form['examples'].values():
                                allowed.update(str(v) for v in example['value'].values())
                            preset = exact_button_union(sorted(allowed), value_cell)
                            expect(preset).to_have_count(1)
                            if preset.inner_text().strip() != str(value):
                                preset.click()
                                option = page.get_by_role('menuitem', name=str(value), exact=True)
                                expect(option).to_have_count(1)
                                option.click()
                            expect(value_cell.get_by_role('button', name=str(value), exact=True)).to_have_count(1)

                    def initialize_form(values):
                        body = client_body()
                        # API-client RequestBody has its own content-type
                        # state. Documentation example selection does not set it.
                        media_labels = ('JSON', 'XML', 'YAML', 'Multipart Form', 'Form URL Encoded', 'Binary File', 'Other', 'None')
                        media_labels += tuple(expected_spec['paths']['/']['post']['requestBody']['content'])
                        media = exact_button_union(media_labels, body)
                        expect(media).to_have_count(1)
                        if media.inner_text().strip() != 'Form URL Encoded':
                            media.click()
                            option = page.get_by_role('option', name='Form URL Encoded', exact=True)
                            expect(option).to_have_count(1)
                            option.click()
                        expect(body.get_by_role('button', name='Form URL Encoded', exact=True)).to_have_count(1)
                        # Initialize a complete approved example through the
                        # real form grid. Disable inherited fields explicitly;
                        # the unchanged wire equality rejects any extra fields.
                        checkboxes = body.get_by_role('checkbox')
                        assert checkboxes.count() <= 200
                        for checkbox in checkboxes.all():
                            checkbox.uncheck()
                        for key, value in values.items():
                            field(key, value)

                    def optional_disable(name):
                        checkbox = page.get_by_role('checkbox', name='Include ' + name + ' in request', exact=True)
                        assert checkbox.count() <= 1
                        if checkbox.count():
                            checkbox.uncheck()

                    def open_example(name):
                        # Accessible selectors follow the existing IAM Scalar precedent.
                        # Metrics-specific DOM contract remains UNRUN: ambiguity fails.
                        page.goto(a.endpoint + '/_simulator/docs/#tag/cloudwatch-metrics/POST/')
                        expect(page.get_by_text('CloudWatch Metrics', exact=True).first).to_be_visible(timeout=30000)
                        # Actual08 diagnostic: one RequestBody media control,
                        # four Responses controls share the same MIME labels.
                        operation = page.locator('section[id="api-1/tag/cloudwatch-metrics/POST/"]')
                        expect(operation).to_have_count(1)
                        request_body = operation.get_by_role('group', name='Request Body', exact=True)
                        expect(request_body).to_have_count(1)
                        # Explicitly select form media first. Root POST defaults
                        # to JSON1.1; a Metrics tag does not preselect its example.
                        media_values = tuple(expected_spec['paths']['/']['post']['requestBody']['content'])
                        media_names = ['Selected Content Type: ' + value for value in media_values]
                        media_selector = exact_button_union(media_names, request_body)
                        expect(media_selector).to_have_count(1)
                        if media_selector.get_attribute('aria-label') != 'Selected Content Type: ' + MEDIA and MEDIA not in media_selector.inner_text():
                            media_selector.click()
                            option = page.get_by_text(MEDIA, exact=True)
                            expect(option).to_have_count(1)
                            option.click()
                        expect(request_body.get_by_role('button', name='Selected Content Type: ' + MEDIA, exact=True)).to_have_count(1)
                        # Admit current selection from the entire reviewed form
                        # inventory (initially DescribeStacks), not only Metrics.
                        form_examples = expected_spec['paths']['/']['post']['requestBody']['content'][MEDIA]['examples']
                        labels = {example.get('summary', key) for key, example in form_examples.items()}
                        selector = exact_button_union(sorted(labels), operation)
                        expect(selector).to_have_count(1)
                        selector.click()
                        choice = page.get_by_text(examples[name]['summary'], exact=True)
                        expect(choice).to_have_count(1)
                        choice.click()
                        test = operation.get_by_role('button', name='Test Request (post /)', exact=True)
                        expect(test).to_have_count(1)
                        test.click()
                        header_preset('X-Simulator-Service', 'monitoring')
                        header_preset('X-Simulator-Region', 'us-east-1')
                        for header in ('X-Amz-Target', 'Authorization', 'X-Simulator-Token'):
                            optional_disable(header)
                        initialize_form(examples[name]['value'])
                        for key in examples[name]['value']:
                            expect(page.get_by_role('checkbox', name='Include ' + key + ' in request', exact=True)).to_be_checked()

                    def send(name, fields, status=200, code=None):
                        nonlocal phase
                        phase = name + '-' + str(width) + '-' + str(status)
                        action = fields['Action']
                        send_button = page.get_by_role('button').filter(has_text=re.compile(r'^Send Request'))
                        expect(send_button).to_have_count(1)
                        before_count = len(transport)
                        with page.expect_response(lambda r: r.request.method == 'POST' and r.url == a.endpoint + '/') as sent:
                            send_button.click()
                        response = sent.value
                        request = response.request
                        actual = parse_qs(request.post_data or '', keep_blank_values=True, strict_parsing=True)
                        assert actual == {k: [str(v)] for k, v in fields.items()}, 'Actual Scalar serialized fields differ from selected served example'
                        headers = request.all_headers()
                        assert headers.get('content-type', '').split(';')[0] == MEDIA
                        assert headers.get('x-simulator-service') == 'monitoring' and headers.get('x-simulator-region') == 'us-east-1'
                        assert not any(k in headers for k in ('authorization', 'x-amz-target', 'x-simulator-token')), 'Stale protocol/auth header was sent'
                        root = xml_response(response, action, status, code)
                        assert len(transport) == before_count + 1 and transport[-1] == status
                        result['sends'].append({'width': width, 'example': name, 'action': action, 'status': status, 'requestFields': sorted(actual), 'requestBodySHA256': digest((request.post_data or '').encode()), 'XMLRoot': root.tag})
                        return root

                    def audit(state):
                        page.evaluate(axe_path.read_text())
                        observation = page.evaluate('async () => {\n    const result = await axe.run(document, {runOnly:{type:\'tag\',values:[\'wcag2a\',\'wcag2aa\',\'wcag21aa\']}});\n    const allowedAttrs = [\'aria-expanded\',\'aria-controls\',\'aria-autocomplete\',\'aria-haspopup\',\'aria-label\',\'aria-labelledby\',\'aria-describedby\',\'aria-hidden\',\'aria-required\',\'aria-invalid\',\'aria-activedescendant\'];\n    const roleNames = [\'combobox\',\'textbox\',\'button\',\'checkbox\',\'group\',\'dialog\',\'row\',\'cell\',\'gridcell\',\'listbox\',\'option\'];\n    const tags = [\'DIV\',\'INPUT\',\'TEXTAREA\',\'BUTTON\',\'SELECT\',\'SPAN\',\'A\',\'TD\',\'TR\'];\n    const safeID = value => typeof value===\'string\' && /^[A-Za-z][A-Za-z0-9_-]{0,63}$/.test(value) ? value : null;\n    let truncated=0, unresolved=0, ambiguous=0, redacted=0;\n    const details=[];\n    for (const kind of [\'violations\',\'incomplete\']) for (const rule of result[kind]) for (const node of rule.nodes) {\n        if(details.length>=64){truncated++;continue;}\n        const target=node.target;\n        const allowed=Array.isArray(target)&&target.length===1&&typeof target[0]===\'string\'&&target[0].length<=512&&!/\\[(?:value|password|authorization|cookie|token)(?:[\\s~|^$*]?=|\\])/i.test(target[0]);\n        let matches=[];\n        try{if(allowed)matches=Array.from(document.querySelectorAll(target[0]));}catch(_){}\n        if(!allowed)redacted++;\n        if(matches.length!==1){if(matches.length>1)ambiguous++;else unresolved++;}\n        const element=matches.length===1?matches[0]:null;\n        const checks=[];\n        for(const group of [\'any\',\'all\',\'none\']) {\n            const values=node[group]||[];\n            if(values.length>8)truncated++;\n            for(const check of values.slice(0,8)) {\n                const names=new Set();let visited=0;\n                function attributes(value,depth=0) {\n                    if(depth>5||++visited>128){truncated++;return;}\n                    if(typeof value===\'string\'){if(allowedAttrs.includes(value))names.add(value);}\n                    else if(Array.isArray(value)){if(value.length>128)truncated++;for(const part of value.slice(0,128))attributes(part,depth+1);}\n                    else if(value&&typeof value===\'object\') {\n                        for(const key of allowedAttrs)if(Object.hasOwn(value,key))names.add(key);\n                        for(const key of [\'missingAttrs\',\'requiredAttrs\',\'prohibitedAttrs\',\'attrs\',\'attributes\'])if(Object.hasOwn(value,key))attributes(value[key],depth+1);\n                    }\n                }\n                attributes(check.data);\n                checks.push({group,id:safeID(check.id),attributeNames:Array.from(names).sort()});\n            }\n        }\n        let shape=null;\n        if(element&&element.ownerDocument===document) {\n            const expanded=element.getAttribute(\'aria-expanded\');\n            const autocomplete=element.getAttribute(\'aria-autocomplete\');\n            const role=element.getAttribute(\'role\');\n            const controls=(element.getAttribute(\'aria-controls\')||\'\').split(/\\s+/).filter(Boolean);\n            if(controls.length>8)truncated++;\n            shape={tag:tags.includes(element.tagName)?element.tagName:\'OTHER\',role:role===null?null:roleNames.includes(role)?role:\'OTHER\',\n                contenteditable:element.hasAttribute(\'contenteditable\'),codeMirrorContent:element.classList.contains(\'cm-content\'),\n                insideDialog:!!element.closest(\'[role="dialog"]\'),insideTableRow:!!element.closest(\'tr\'),\n                presentAriaAttributes:allowedAttrs.filter(name=>element.hasAttribute(name)),\n                expanded:[\'true\',\'false\'].includes(expanded)?expanded:null,\n                autocomplete:[\'none\',\'list\',\'inline\',\'both\'].includes(autocomplete)?autocomplete:null,\n                controlsCount:controls.length,controlsResolve:controls.length<=8?controls.map(id=>!!document.getElementById(id)):null};\n        }\n        details.push({kind,ruleID:safeID(rule.id),targetIndex:details.length,resolved:!!shape,shape,checks});\n    }\n    return {violations:result.violations.map(rule=>({id:safeID(rule.id),nodes:rule.nodes.length})),\n        incomplete:result.incomplete.map(rule=>({id:safeID(rule.id),nodes:rule.nodes.length})),passes:result.passes.length,\n        boundedTargets:{maxTargets:64,maxChecksPerGroup:8,details,truncated,unresolved,ambiguous,redacted,\n            privacy:\'Closed roles/tags/ARIA names and booleans only; no target selectors, text, HTML, input/header/cookie/token values.\'}};\n}\n')
                        check = {'violations': observation['violations'], 'passes': observation['passes']}
                        capture_error = None
                        try:
                            (reports / ('metrics-reference-%s-%s-axe.json' % (width, state))).write_text(json.dumps(observation, indent=2) + '\n')
                        except OSError as error:
                            capture_error = error
                            result.setdefault('auditCaptureErrors', []).append(type(error).__name__)
                        assert check['violations'] == [], 'Strict reference axe violation'
                        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth + 1'), 'Reference horizontal reflow failure'
                        if capture_error is not None:
                            raise capture_error
                        result['audits'].append({'width': width, 'state': state, **check})
                        page.screenshot(path=str(reports / ('metrics-reference-%s-%s.png' % (width, state))), full_page=False)

                    phase = 'put-' + str(width)
                    open_example(EXAMPLES[0])
                    audit('put-form')
                    before_put = datetime.now(timezone.utc)
                    send(EXAMPLES[0], examples[EXAMPLES[0]]['value'])
                    after_put = datetime.now(timezone.utc)
                    assert native.frame(b.db) == peer_frame
                    with sqlite3.connect('file:' + str(a.db) + '?mode=ro', uri=True) as db:
                        rows = db.execute('SELECT s.namespace,s.name,p.unit,p.document FROM aws_metrics_points p JOIN aws_metrics_series s ON p.region=s.region AND p.series_id=s.identity').fetchall()
                        assert len(rows) == 1 and rows[0][:3] == ('Simulator/Reference', 'Requests', 'Count')
                        stored = json.loads(rows[0][3])
                        assert stored['value'] == 7, 'Actual persisted sample must equal literal template value'
                    stable = native.frame(a.db)
                    assert stable != local_frame
                    open_example(EXAMPLES[1])
                    root = send(EXAMPLES[1], examples[EXAMPLES[1]]['value'])
                    metrics = root.findall(NS + 'ListMetricsResult/' + NS + 'Metrics/' + NS + 'member')
                    assert len(metrics) == 1
                    metric = metrics[0]
                    assert metric.findtext(NS + 'Namespace') == 'Simulator/Reference' and metric.findtext(NS + 'MetricName') == 'Requests'
                    assert [(d.findtext(NS+'Name'), d.findtext(NS+'Value')) for d in metric.findall(NS+'Dimensions/'+NS+'member')] == [('Stage', 'local')]
                    assert native.frame(a.db) == stable and native.frame(b.db) == peer_frame
                    open_example(EXAMPLES[2])
                    send(EXAMPLES[2], examples[EXAMPLES[2]]['value'], 400, 'InvalidParameterValue')
                    assert native.frame(a.db) == stable and native.frame(b.db) == peer_frame
                    audit('invalid-time')
                    start = min(peer_seed_time, before_put).replace(second=0, microsecond=0) - timedelta(minutes=1)
                    end = after_put.replace(second=0, microsecond=0) + timedelta(minutes=2)
                    replacement = dict(examples[EXAMPLES[2]]['value'], StartTime=start.isoformat(), EndTime=end.isoformat())
                    field('StartTime', replacement['StartTime']); field('EndTime', replacement['EndTime'])
                    root = send(EXAMPLES[2], replacement)
                    stamp = typed_row(root)
                    assert start <= stamp < end
                    points = sdk.get_metric_statistics(Namespace='Simulator/Reference', MetricName='Requests', Dimensions=dims, StartTime=start, EndTime=end, Period=60, Statistics=list(STATS), Unit='Count')['Datapoints']
                    assert len(points) == 1 and {k: points[0][k] for k in STATS} == STATS
                    other = peer.get_metric_statistics(Namespace='Simulator/Reference', MetricName='Requests', Dimensions=dims, StartTime=start, EndTime=end, Period=60, Statistics=list(STATS), Unit='Count')['Datapoints']
                    assert len(other) == 1 and other[0]['Sum'] == 101
                    assert native.frame(a.db) == stable and native.frame(b.db) == peer_frame
                    audit('statistics')
                    # Actual bound read-only session: routing headers cannot grant Put.
                    assert context.request.post(a.endpoint + '/_simulator/login', form={'token': reader}, max_redirects=0).status == 303
                    open_example(EXAMPLES[0])
                    send(EXAMPLES[0], examples[EXAMPLES[0]]['value'], 403, 'AccessDenied')
                    assert native.frame(a.db) == stable and native.frame(b.db) == peer_frame
                    audit('reader-denied')
                    open_example(EXAMPLES[1]); send(EXAMPLES[1], examples[EXAMPLES[1]]['value'])
                    assert native.frame(a.db) == stable and native.frame(b.db) == peer_frame
                    assert not outside and not errors
                    result.setdefault('browserCounts', []).append({'width': width, 'nativePOSTResponses': len(transport), 'outside': len(outside), 'pageErrors': len(errors)})
            assert all(c['calls'] == c['sends'] == c['responses'] for c in native.COUNTERS)
            result['SDKCounters'] = native.COUNTERS
        result['cleanup'] = True
        result['status'] = 'WORK_COMPLETE_POSTGUARDS_PENDING'
    except BaseException as error:
        result['status'] = 'FAIL'
        result['failure'] = {'phase': phase, 'type': type(error).__name__}
        raise
    finally:
        # A recorded false flag is not an admission guard. Never publish PASS
        # until every owned input is rechecked, including read/hash failures.
        # If the operation already failed, preserve its original exception.
        primary_failed = result['status'] == 'FAIL'
        postguard_failures = []
        for label, path, expected in (
            ('binary', binary, result['binarySHA256']),
            ('spec', Path('internal/server/apidocs/openapi.json'), result['specSHA256']),
            ('helper', Path(__file__), result['helperSHA256']),
        ):
            try:
                exact = digest(path.read_bytes()) == expected
            except Exception as error:
                exact = False
                postguard_failures.append({'input': label, 'type': type(error).__name__})
            else:
                if not exact:
                    postguard_failures.append({'input': label, 'type': 'SHA256Mismatch'})
            result[label + 'PostExact'] = exact
        if postguard_failures:
            result['status'] = 'FAIL'
            result['postGuardFailures'] = postguard_failures
            if not primary_failed:
                result['failure'] = {'phase': 'postguards', 'type': 'InputPostGuardFailure'}
        elif not primary_failed:
            result['status'] = 'PASS'
        (reports / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        if postguard_failures and not primary_failed:
            raise AssertionError('Owned input postguard failed; receipt preserved')


if __name__ == '__main__':
    run(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator'), os.environ.get('SIMULATOR_REPORTS', 'reports/metrics-reference'))
