"""Draft a catalog Service spec from an official botocore model.

Development-only helper. The output is a starting point that must be reviewed
against the official shapes; it never runs in the simulator.
Usage: python tools/awsmodels/draft.py <botocore-name> <go-var> <title> <issue> [iam-prefix]
"""
import sys, re, json, botocore.session
s = botocore.session.get_session()
svc = sys.argv[1]; var = sys.argv[2]; title = sys.argv[3]; issue = sys.argv[4]; prefix = sys.argv[5] if len(sys.argv) > 5 else None
m = s.get_service_model(svc)
prefix = prefix or m.metadata.get('signingName') or m.metadata['endpointPrefix']
ops = set(m.operation_names)
try: pages = s.get_paginator_model(svc)._paginator_config
except Exception: pages = {}
def plural(x):
    return [x + 's', x + 'es', x[:-1] + 'ies' if x.endswith('y') else None]
nouns = sorted({o[6:] for o in ops if o.startswith('Create')})
lines = []
res_lines = []
for noun in nouns:
    create = 'Create' + noun
    read = next((p + noun for p in ('Get', 'Describe') if p + noun in ops), None)
    lst = next((p + n for n in plural(noun) if n for p in ('List', 'Describe') if p + n in ops), None)
    upd = next((p + noun for p in ('Update', 'Modify', 'Put') if p + noun in ops), None)
    dele = next((p + noun for p in ('Delete', 'Remove') if p + noun in ops), None)
    if not (dele and (read or lst)): continue
    shape = None
    if read:
        out = m.operation_model(read).output_shape
        if out:
            structs = [v for v in out.members.values() if v.type_name == 'structure']
            shape = structs[0].name if len(structs) == 1 and len(out.members) <= 2 else out.name
    summary = None
    if lst:
        out = m.operation_model(lst).output_shape
        lists = [v for v in out.members.values() if v.type_name == 'list' and v.member.type_name == 'structure']
        if lists:
            summary = lists[0].member.name
            if not shape: shape = summary
    cin = m.operation_model(create).input_shape
    cout = m.operation_model(create).output_shape
    rshape = m.shape_for(shape) if shape else None
    rm = list(rshape.members) if rshape else []
    arn = next((k for k in rm if re.fullmatch(r'(?i).*arn', k) and noun.lower() in k.lower()), next((k for k in rm if k.lower() == 'arn'), None))
    name = next((k for k in rm if k.lower() in (noun.lower() + 'name', 'name')), None)
    idm = next((k for k in rm if k.lower() in (noun.lower() + 'id', 'id')), None)
    req = cin.required_members if cin else []
    lookup = None
    for o in (read, upd, dele):
        if o:
            ins = m.operation_model(o).input_shape
            if ins:
                cand = [k for k in ins.required_members]
                lookup = lookup or (cand[0] if cand else None)
    res_lines.append(f'\t\t// {noun}: create in={req} createOut={list(cout.members) if cout else []} shape={shape} members={rm[:14]}')
    res_lines.append(f'\t\t{{Kind: "{noun[0].lower()+noun[1:]}", Shape: "{shape}", Summary: "{summary if summary and summary != shape else ""}", ID: "{"name" if name and not idm else "uuid"}", ARN: "arn:{{partition}}:{prefix}:{{region}}:{{account}}:{noun.lower()}/{{id}}", ARNMember: "{arn or ""}", NameMember: "{name or ""}", IDMember: "{idm or ""}", Unique: true}},')
    for kind, op in (('Create', create), ('Read', read), ('List', lst), ('Update', upd), ('Delete', dele)):
        if not op: continue
        ins = m.operation_model(op).input_shape
        look = ''
        if kind in ('Read', 'Update', 'Delete') and ins:
            look = f', Lookup: "{ins.required_members[0] if ins.required_members else "?"}"'
        lines.append(f'\t\t{{Name: "{op}", Kind: {kind}, Resource: "{noun[0].lower()+noun[1:]}"{look}}},  // in={list(ins.members) if ins else []}')
for op, kind in (('TagResource', 'Tag'), ('UntagResource', 'Untag'), ('ListTagsForResource', 'ListTags')):
    if op in ops:
        ins = m.operation_model(op).input_shape
        lines.append(f'\t\t{{Name: "{op}", Kind: {kind}, Lookup: "{[k for k in ins.members if "rn" in k.lower() or "resource" in k.lower()][0]}"}},')
print(f'var {var} = &Service{{\n\tModel: "{svc}", Title: "{title}", IAMPrefix: "{prefix}", Issues: []int{{{issue}}},\n\tResources: []*Resource{{')
print('\n'.join(res_lines))
print('\t},\n\tOps: []Op{')
print('\n'.join(lines))
print('\t},\n\tBounds: []string{},\n}')
print('// official ops:', len(ops), file=sys.stderr)
