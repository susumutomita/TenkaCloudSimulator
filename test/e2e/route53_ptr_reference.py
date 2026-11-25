"""Reverse only the reviewed one-literal PTR documentation addition for historical hashes."""

def project_before_ptr(paths):
    entry = paths.get('/2013-04-01/hostedzone/{Id}/rrset', {}).get('post', {})
    current = 'simple A/AAAA/CNAME/TXT/MX/PTR only'
    previous = 'simple A/AAAA/CNAME/TXT/MX only'
    description = entry.get('description', '')
    assert description.count(current) == 1, 'Route53 exact path/request/response/schema contract changed: PTR record types'
    entry['description'] = description.replace(current, previous)
