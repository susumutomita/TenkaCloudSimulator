"""Pin the complete definition-only SSM document reference before projection."""
import hashlib
import json
import runpy
from pathlib import Path
project_ssm_commands = runpy.run_path(str(Path(__file__).with_name("ssm_command_reference.py")))["project_ssm_commands"]
TEXT='SSM documents: CreateDocument, GetDocument, DescribeDocument, ListDocuments,\nUpdateDocument and DeleteDocument manage local definitions only. Stored Command\nschema 2.2 JSON contains bounded literal aws:runShellScript steps but is never\nexecuted by these APIs. Status Active means the definition is stored, not that\ncommands ran. No SendCommand, inventory or sessions are added.\n\nAll calls require the existing host session/token and action-specific IAM on\narn:aws:ssm:<region>:000000000000:document/<name>; ListDocuments uses *. Update\nrequires both ssm:UpdateDocument and ssm:GetDocument. Create starts version 1;\nUpdate appends a version only when DocumentVersion is current or $LATEST.\nDefault remains 1. Get/Describe accept $DEFAULT/$LATEST or a saved version.\nDelete removes all versions and returns HTTP 200 with an empty body. Examples\nare static and destructive examples must use disposable local definitions.\n\nOnly the declared input fields are accepted. Names 3..128 ASCII letters/digits/\nunderscore/hyphen/dot, no aws/amazon/amzn prefix. Content <=64 KiB, envelope <=512\nKiB, global <=100 documents/8 MiB content, <=100 versions/document. List page\nsize 1..50; tokens bind caller/region/page size/current snapshot and expire by\nmutation. Unsupported formats, tags, attachments, roles, sharing, interpolation,\nversion-specific deletion and other schema features fail rather than being\nsilently ignored. Local transactions/pagination/quota are simulator contracts,\nnot promises of identical AWS behavior. No batch console UI or document console\nis added by this increment.'
OPS=['CreateDocument', 'GetDocument', 'DescribeDocument', 'ListDocuments', 'UpdateDocument', 'DeleteDocument']
SCHEMAS=['SSMCreateDocumentInput', 'SSMGetDocumentInput', 'SSMDescribeDocumentInput', 'SSMListDocumentsInput', 'SSMUpdateDocumentInput', 'SSMDeleteDocumentInput', 'SSMDocumentDescription', 'SSMDocumentIdentifier', 'SSMCreateDocumentOutput', 'SSMUpdateDocumentOutput', 'SSMDescribeDocumentOutput', 'SSMGetDocumentOutput', 'SSMListDocumentsOutput']
OUTPUTS=['SSMCreateDocumentOutput', 'SSMUpdateDocumentOutput', 'SSMDescribeDocumentOutput', 'SSMGetDocumentOutput', 'SSMListDocumentsOutput']

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
    assert hashlib.sha256(json.dumps(delta,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()=='6526d6350f0389920d8ed23c8420424d94805eabe47d084894cf3e0b73938d72','SSM document reference delta changed'
    suffix='\n\n'+TEXT
    assert post['description'].endswith(suffix) and post['description'].count(TEXT)==1,'SSM document description changed'
    post['description']=post['description'][:-len(suffix)]
    for media, schemas in [(request,[name+'Input' for name in names]),(response,OUTPUTS)]:
        refs=[{'$ref':'#/components/schemas/'+name} for name in schemas]
        union=media['schema']['anyOf']
        assert union[-len(refs):]==refs and all(union.count(ref)==1 for ref in refs),'SSM document union changed'
        del union[-len(refs):]
