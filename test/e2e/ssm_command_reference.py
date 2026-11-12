"""Validate the bounded opt-in RunCommand reference before inherited projection."""
import hashlib,json
TEXT = 'Opt-in native RunCommand: SendCommand, GetCommandInvocation and ListCommands require the configured canonical guest backend, host access token and verified assumed-role SigV4 SSM credentials. They are unavailable by default; console cookies or dummy signature credentials alone cannot invoke them. Only AWS-RunShellScript is accepted; saved custom document definitions remain non-executable. SendCommand can execute guest shell code and is intentionally not prefilled as a runnable example. It requires ssm:SendCommand on the AWS document ARN and every instance, from one current policy snapshot. Targets must share one retained deployment. List/Get require their IAM action on * and additionally restrict visibility to the current role incarnation and region (a local restriction, not AWS account-wide parity). ListCommands requires exactly one InvokedAfter RFC3339 filter, page size 1..50, and immutable ten-minute cursors. Command quota 128 retained; at most eight targets, 32 script strings totaling 8192 UTF-8 bytes, 4096 output bytes per stream. Unknown results are errors, never successful execution; discover existing commands instead of resubmitting after an uncertain response. No CancelCommand, arbitrary documents, session-manager-plugin compatibility or real-guest acceptance is implied. The document-management section above describes a separate definition-only API.'
OPS = ['SendCommand', 'GetCommandInvocation', 'ListCommands']
SCHEMAS = ['SSMSendCommandInput', 'SSMGetCommandInvocationInput', 'SSMListCommandsInput', 'SSMCommandSummary', 'SSMSendCommandOutput', 'SSMListCommandsOutput', 'SSMGetCommandInvocationOutput']

def project_ssm_commands(spec):
    post=spec['paths']['/']['post']
    delta={'description':TEXT,'mapping':post.pop('x-simulator-ssm-command-operation-schemas',None),'schemas':{n:spec['components']['schemas'].pop(n,None) for n in SCHEMAS}}
    assert hashlib.sha256(json.dumps(delta,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()=='6b3e64de5d96c1489d6c3351af41b5db8d85dbea59ef3493aea9ea7151e304f9','RunCommand contract changed'
    suffix='\n\n'+TEXT
    assert post['description'].endswith(suffix) and post['description'].count(TEXT)==1
    post['description']=post['description'][:-len(suffix)]
    for section,suffix in [(post['requestBody'],'Input'),(post['responses']['200'],'Output')]:
        media=section['content']['application/x-amz-json-1.1']
        refs=[{'$ref':'#/components/schemas/SSM'+op+suffix} for op in OPS]
        union=media['schema']['anyOf']
        assert union[-3:]==refs and all(union.count(r)==1 for r in refs)
        del union[-3:]
        assert all('SSM'+op not in media.get('examples',{}) for op in OPS),'No prefilled executable command examples'
