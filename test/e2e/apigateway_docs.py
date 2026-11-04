"""Actual embedded Scalar controls create/list the same native REST state."""
from playwright.sync_api import expect
from check import REPORTS

def verify_rest_docs(page,endpoint,gateway):
 page.goto(endpoint+'/_simulator/docs/#tag/api-gateway-rest-apis/POST/restapis')
 expect(page.get_by_text('Create Regional REST API',exact=True).first).to_be_visible(timeout=30000)
 page.get_by_role('button',name='Test Request (post /restapis)',exact=True).click()
 with page.expect_response(lambda response:response.request.method=='POST' and response.url==endpoint+'/restapis') as created:
  page.get_by_role('button').filter(has_text='Send Request').last.click()
 assert created.value.status==201,created.value.text()
 native=created.value.json();assert native['name']=='example-rest';assert gateway.get_rest_api(restApiId=native['id'])['name']=='example-rest'
 page.screenshot(path=str(REPORTS/'apigateway-scalar-created.png'),full_page=False)
 page.get_by_role('button',name='Close Client',exact=True).click()
 page.goto(endpoint+'/_simulator/docs/#tag/api-gateway-rest-apis/GET/restapis')
 page.get_by_role('button',name='Test Request (get /restapis)',exact=True).click()
 with page.expect_response(lambda response:response.request.method=='GET' and response.url.split('?')[0]==endpoint+'/restapis') as listed:
  page.get_by_role('button').filter(has_text='Send Request').last.click()
 assert listed.value.status==200 and any(item['id']==native['id'] for item in listed.value.json()['item'])
 assert any(item['id']==native['id'] for item in gateway.get_rest_apis()['items'])
 gateway.delete_rest_api(restApiId=native['id'])
 page.get_by_role('button',name='Close Client',exact=True).click()
 page.goto(endpoint+'/_simulator/docs/#tag/s3-basic-object-storage/GET/')
 page.get_by_role('button',name='Test Request (get /)',exact=True).click()
 with page.expect_response(lambda response:response.request.method=='GET' and response.url.rstrip('/')==endpoint) as buckets:
  page.get_by_role('button').filter(has_text='Send Request').last.click()
 assert buckets.value.status==200 and 'ListAllMyBucketsResult' in buckets.value.text()
