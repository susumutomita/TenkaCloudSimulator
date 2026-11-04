package server

import (
	"testing"
)

func TestActualLambdaSDKUsesExecutionRoleAndSharedSSMAndS3(t *testing.T) {
	s := instance(t)
	role := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ssm:GetParameter","ssm:GetParameters"],"Resource":"arn:aws:ssm:us-east-1:000000000000:parameter/sdk-own/*"},{"Effect":"Allow","Action":["s3:PutObject","s3:GetObject","s3:DeleteObject"],"Resource":"arn:aws:s3:::sdk-evidence/*"}]}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/executor/policy", role, 200, nil)
	for name, value := range map[string]string{"/sdk-own/value": "from-sdk", "/sdk-private/value": "private-dummy"} {
		ssmCall(t, s, "PutParameter", map[string]any{"Name": name, "Type": "String", "Value": value}, 200, nil)
	}
	status(t, s, "PUT", "/sdk-evidence", "", 200, nil)
	source := `import boto3
from botocore.exceptions import ClientError
from _simulator_bridge import call
ssm=boto3.client('ssm')
s3=boto3.client('s3')
def handler(event, context):
    value=ssm.get_parameter(Name='/sdk-own/value')['Parameter']['Value']
    multiple=ssm.get_parameters(Names=['/sdk-own/value'])['Parameters'][0]['Value']
    s3.put_object(Bucket='sdk-evidence',Key='incident.txt',Body=value,ContentType='text/plain')
    read=s3.get_object(Bucket='sdk-evidence',Key='incident.txt')['Body'].read().decode()
    denied=''
    try:
        ssm.get_parameter(Name='/sdk-private/value')
    except ClientError as error:
        denied=error.response['Error']['Code']
    forged=call({'service':'ssm','operation':'GetParameter','parameters':{'Name':'/sdk-private/value'},'principal':'simulator-admin'})
    blocked=False
    try:
        boto3.client('s3',endpoint_url='http://127.0.0.1:1')
    except NotImplementedError:
        blocked=True
    return {'value':value,'multiple':multiple,'object':read,'denied':denied,'forged':forged['error']['code'],'endpointBlocked':blocked}
`
	input := lambdaInput(t, "sdk-function", source)
	input["Timeout"] = 15
	lambdaCall(t, s, "POST", functionRoot, input, 201, nil)
	result := lambdaCall(t, s, "POST", functionRoot+"/sdk-function/invocations", map[string]any{}, 200, nil)
	if result["value"] != "from-sdk" || result["multiple"] != "from-sdk" || result["object"] != "from-sdk" || result["denied"] != "AccessDeniedException" || result["forged"] != "InvalidRequest" || result["endpointBlocked"] != true {
		t.Fatal(result)
	}
	data := status(t, s, "GET", "/sdk-evidence/incident.txt", "", 200, nil)
	if string(data) != "from-sdk" {
		t.Fatal(string(data))
	}
	ss := ssmCall(t, s, "GetParameter", map[string]any{"Name": "/sdk-private/value"}, 200, nil)
	if ss["Parameter"].(map[string]any)["Value"] != "private-dummy" {
		t.Fatal("private parameter changed")
	}
}
