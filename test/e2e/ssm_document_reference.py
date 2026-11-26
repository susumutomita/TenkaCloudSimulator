"""Pin the complete definition-only SSM document reference before projection."""
import hashlib
import json
import runpy
from pathlib import Path
project_ssm_commands = runpy.run_path(str(Path(__file__).with_name("ssm_command_reference.py")))["project_ssm_commands"]
TEXT='SSM documents: CreateDocument, GetDocument, DescribeDocument, ListDocuments,\nListDocumentVersions, UpdateDocument and DeleteDocument manage definitions only. Stored Command\nschema 2.2 JSON contains bounded literal aws:runShellScript steps but is never\nexecuted by these APIs. Status Active means the definition is stored, not that\ncommands ran. No SendCommand, inventory or sessions are added.\n\nAll calls require the existing host session/token and action-specific IAM on\narn:aws:ssm:<region>:000000000000:document/<name>; ListDocuments uses *. Update\nrequires both ssm:UpdateDocument and ssm:GetDocument. Create starts version 1;\nUpdate appends a version only when DocumentVersion is current or $LATEST.\nDefault remains 1. Get/Describe accept $DEFAULT/$LATEST or a saved version.\nDelete removes all versions and returns HTTP 200 with an empty body. Examples\nare static and destructive examples must use disposable local definitions.\n\nOnly the declared input fields are accepted. Names 3..128 ASCII letters/digits/\nunderscore/hyphen/dot, no aws/amazon/amzn prefix. Content <=64 KiB, envelope <=512\nKiB, global <=100 documents/8 MiB content, <=100 versions/document. List page\nsize 1..50; tokens bind caller/region/page size/current snapshot and expire by\nmutation. Unsupported formats, tags, attachments, roles, sharing, interpolation,\nversion-specific deletion and other schema features fail rather than being\nsilently ignored. Local transactions/pagination/quota are simulator contracts,\nnot promises of identical AWS behavior. ListDocumentVersions accepts a local\nName, with optional MaxResults and NextToken, and returns version metadata only.\nAn update invalidates a cursor for that document, not other documents. Missing\ndocuments return InvalidDocument. Supply the document name and header manually\nwhen using this example; the preset does not create anything.'
OPS=['CreateDocument', 'GetDocument', 'DescribeDocument', 'ListDocuments', 'ListDocumentVersions', 'UpdateDocument', 'DeleteDocument']
SCHEMAS=['SSMCreateDocumentInput', 'SSMGetDocumentInput', 'SSMDescribeDocumentInput', 'SSMListDocumentsInput', 'SSMUpdateDocumentInput', 'SSMDeleteDocumentInput', 'SSMDocumentDescription', 'SSMDocumentIdentifier', 'SSMCreateDocumentOutput', 'SSMUpdateDocumentOutput', 'SSMDescribeDocumentOutput', 'SSMGetDocumentOutput', 'SSMListDocumentsOutput', 'SSMListDocumentVersionsInput', 'SSMDocumentVersionInfo', 'SSMListDocumentVersionsOutput']
OUTPUTS=['SSMCreateDocumentOutput', 'SSMUpdateDocumentOutput', 'SSMDescribeDocumentOutput', 'SSMGetDocumentOutput', 'SSMListDocumentsOutput', 'SSMListDocumentVersionsOutput']

def project_before_ssm_document_versions(spec):
    """Undo only the reviewed ListDocumentVersions increment in a historical copy."""
    post = spec['paths']['/']['post']
    media_type = 'application/x-amz-json-1.1'
    request = post['requestBody']['content'][media_type]
    response = post['responses']['200']['content'][media_type]
    name = 'SSMListDocumentVersions'
    shapes = (name + 'Input', 'SSMDocumentVersionInfo', name + 'Output')
    schemas = spec['components']['schemas']
    mapping = post.get('x-simulator-ssm-document-operation-schemas')
    assert isinstance(mapping, dict), 'SSM ListDocumentVersions operation map missing'
    delta = {
        'mapping': mapping.pop('AmazonSSM.ListDocumentVersions', None),
        'request': request['examples'].pop(name, None),
        'response': response['examples'].pop(name, None),
        'schemas': {shape: schemas.pop(shape, None) for shape in shapes},
    }
    exact = hashlib.sha256(json.dumps(delta, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()
    assert exact == '06b42e935e7226e6a5365f5d6e7c1113d36940155084e4b9c3e6a70a050fbf6e', 'SSM ListDocumentVersions reference changed'

    for media, suffix in ((request, 'Input'), (response, 'Output')):
        anchor = {'$ref': '#/components/schemas/SSMListDocuments' + suffix}
        version = {'$ref': '#/components/schemas/' + name + suffix}
        union = media['schema']['anyOf']
        assert union.count(anchor) == 1 and union.count(version) == 1, 'SSM ListDocumentVersions union binding changed'
        index = union.index(anchor) + 1
        assert union[index:index + 1] == [version], 'SSM ListDocumentVersions union order changed'
        union.pop(index)

    first = 'ListDocumentVersions, UpdateDocument and DeleteDocument manage definitions only.'
    tail = (' ListDocumentVersions accepts a local\n'
            'Name, with optional MaxResults and NextToken, and returns version metadata only.\n'
            'An update invalidates a cursor for that document, not other documents. Missing\n'
            'documents return InvalidDocument. Supply the document name and header manually\n'
            'when using this example; the preset does not create anything.')
    assert TEXT.count(first) == 1 and TEXT.endswith(tail), 'SSM ListDocumentVersions projection format changed'
    historical = TEXT.removesuffix(tail).replace(
        first, 'UpdateDocument and DeleteDocument manage local definitions only.', 1)
    historical += ' No batch console UI or document console\nis added by this increment.'
    current = '\n\n' + TEXT
    assert post['description'].count(current) == 1, 'SSM ListDocumentVersions description changed'
    post['description'] = post['description'].replace(current, '\n\n' + historical, 1)


def project_ssm_documents(spec):
    project_ssm_commands(spec)
    post=spec['paths']['/']['post']
    request=post['requestBody']['content']['application/x-amz-json-1.1']
    response=post['responses']['200']['content']['application/x-amz-json-1.1']
    names=['SSM'+op for op in OPS]
    delta={'description':TEXT,'mapping':post.pop('x-simulator-ssm-document-operation-schemas',None),
           'schemas':{name:spec['components']['schemas'].pop(name,None) for name in SCHEMAS},
           'requests':{name:request['examples'].pop(name,None) for name in names},
           'responses':{name:response['examples'].pop(name,None) for name in names if name!='SSMDeleteDocument'}}
    assert hashlib.sha256(json.dumps(delta,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()=='26f494aa7727c148c7af1019042c5f6a9cf6166c1b723847db00075cc7640cab','SSM document reference delta changed'
    suffix='\n\n'+TEXT
    assert post['description'].endswith(suffix) and post['description'].count(TEXT)==1,'SSM document description changed'
    post['description']=post['description'][:-len(suffix)]
    for media, schemas in [(request,[name+'Input' for name in names]),(response,OUTPUTS)]:
        refs=[{'$ref':'#/components/schemas/'+name} for name in schemas]
        union=media['schema']['anyOf']
        assert union[-len(refs):]==refs and all(union.count(ref)==1 for ref in refs),'SSM document union changed'
        del union[-len(refs):]
