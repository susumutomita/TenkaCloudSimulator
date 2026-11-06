"""Exercise the actual embedded Scalar form, rather than issuing a substitute fetch."""
from playwright.sync_api import expect
from check import REPORTS


def verify_native_iam_docs(page, endpoint, iam):
 page.goto(endpoint+'/_simulator/docs/#tag/iam-roles/POST/_simulator/providers/aws/')
 expect(page.get_by_text('Manage IAM roles, customer policies and role attachments',exact=True).first).to_be_visible(timeout=30000)
 page.get_by_role('button').filter(has_text='Test Request').filter(has_text='/_simulator/providers/aws/').last.click()
 expect(page.get_by_role('checkbox',name='Include RoleName in request',exact=True)).to_be_checked()
 expect(page.get_by_role('checkbox',name='Include AssumeRolePolicyDocument in request',exact=True)).to_be_checked()
 with page.expect_response(lambda response:response.request.method=='POST' and '/_simulator/providers/aws/' in response.url) as created:
  page.get_by_role('button').filter(has_text='Send Request').last.click()
 assert created.value.status==200,created.value.text()
 assert iam.get_role(RoleName='practice-executor')['Role']['AssumeRolePolicyDocument']['Statement'][0]['Principal']['Service']=='lambda.amazonaws.com'
 page.screenshot(path=str(REPORTS/'native-iam-scalar-executed.png'),full_page=False)
 page.get_by_role('button',name='Close Client',exact=True).click()
 page.get_by_role('button',name='CreateRole (IAM service header required)',exact=True).last.click()
 page.get_by_text('ListRoles (IAM service header required)',exact=True).last.click()
 page.get_by_role('button').filter(has_text='Test Request').filter(has_text='/_simulator/providers/aws/').last.click()
 expect(page.get_by_role('checkbox',name='Include RoleName in request',exact=True)).to_have_count(0)
 with page.expect_response(lambda response:response.request.method=='POST' and '/_simulator/providers/aws/' in response.url) as listed:
  page.get_by_role('button').filter(has_text='Send Request').last.click()
 assert listed.value.status==200,listed.value.text()
 assert 'ListRolesResponse' in listed.value.text() and 'practice-executor' in listed.value.text()
 iam.delete_role(RoleName='practice-executor')
 page.get_by_role('button',name='Close Client',exact=True).click()
 page.goto(endpoint+'/_simulator/docs/#tag/s3-basic-object-storage/GET/')
 page.get_by_role('button',name='Test Request (get /)',exact=True).click()
 with page.expect_response(lambda response:response.request.method=='GET' and response.url.rstrip('/')==endpoint) as buckets:
  page.get_by_role('button').filter(has_text='Send Request').last.click()
 assert buckets.value.status==200 and 'ListAllMyBucketsResult' in buckets.value.text()

