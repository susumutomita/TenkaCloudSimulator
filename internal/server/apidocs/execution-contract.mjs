// First-party native root execution contracts. Not inferred from Scalar tags.
// Explicit arrays mirror server capabilities/dispatch; tests must detect drift.
export const NATIVE = Object.freeze({
  "ssm": {
    "title": "Systems Manager",
    "titleJA": "システムマネージャー",
    "protocol": "1.1",
    "target": "AmazonSSM",
    "operations": [
      "PutParameter",
      "GetParameter",
      "GetParameters",
      "GetParametersByPath",
      "DescribeParameters",
      "DeleteParameter"
    ],
    "templates": {
      "GetParameter": {
        "Name": "/practice/hello"
      },
      "PutParameter": {
        "Name": "/reference/practice",
        "Type": "String",
        "Value": "dummy practice"
      },
      "GetParameters": {
        "Names": [
          "/reference/practice"
        ]
      },
      "GetParametersByPath": {
        "Path": "/reference",
        "Recursive": true
      },
      "DescribeParameters": {},
      "DeleteParameter": {
        "Name": "/reference/practice"
      }
    }
  },
  "sqs": {
    "title": "SQS",
    "titleJA": "SQS",
    "protocol": "1.0",
    "target": "AmazonSQS",
    "operations": [
      "CreateQueue",
      "ListQueues",
      "GetQueueUrl",
      "DeleteQueue",
      "SendMessage",
      "ReceiveMessage",
      "DeleteMessage",
      "ChangeMessageVisibility",
      "GetQueueAttributes"
    ],
    "templates": {
      "CreateQueue": {
        "QueueName": "api-reference-queue",
        "Attributes": {
          "VisibilityTimeout": "30",
          "DelaySeconds": "0",
          "MessageRetentionPeriod": "60"
        }
      },
      "ListQueues": {
        "QueueNamePrefix": "api-reference-queue",
        "MaxResults": 25
      },
      "GetQueueUrl": {
        "QueueName": "api-reference-queue"
      },
      "DeleteQueue": {
        "QueueUrl": "__QUEUE_URL__"
      },
      "SendMessage": {
        "QueueUrl": "__QUEUE_URL__",
        "MessageBody": "Actual API reference message",
        "DelaySeconds": 0
      },
      "ReceiveMessage": {
        "QueueUrl": "__QUEUE_URL__",
        "MaxNumberOfMessages": 1,
        "VisibilityTimeout": 30,
        "WaitTimeSeconds": 0,
        "MessageSystemAttributeNames": [
          "All"
        ],
        "MessageAttributeNames": [
          "All"
        ]
      },
      "DeleteMessage": {
        "QueueUrl": "__QUEUE_URL__",
        "ReceiptHandle": "__RECEIPT_HANDLE__"
      },
      "ChangeMessageVisibility": {
        "QueueUrl": "__QUEUE_URL__",
        "ReceiptHandle": "__RECEIPT_HANDLE__",
        "VisibilityTimeout": 30
      },
      "GetQueueAttributes": {
        "QueueUrl": "__QUEUE_URL__",
        "AttributeNames": [
          "All"
        ]
      }
    }
  },
  "logs": {
    "title": "CloudWatch Logs",
    "titleJA": "CloudWatch Logs",
    "protocol": "1.1",
    "target": "Logs_20140328",
    "operations": [
      "CreateLogGroup",
      "DescribeLogGroups",
      "DeleteLogGroup",
      "CreateLogStream",
      "DescribeLogStreams",
      "PutLogEvents",
      "GetLogEvents"
    ],
    "templates": {
      "CreateLogGroup": {
        "logGroupName": "/aws/reference-example",
        "logGroupClass": "STANDARD"
      },
      "DescribeLogGroups": {
        "logGroupNamePrefix": "/aws/",
        "limit": 50
      },
      "DeleteLogGroup": {
        "logGroupName": "/aws/reference-example"
      },
      "CreateLogStream": {
        "logGroupName": "/aws/reference-example",
        "logStreamName": "application"
      },
      "DescribeLogStreams": {
        "logGroupName": "/aws/reference-example",
        "orderBy": "LogStreamName",
        "limit": 50
      },
      "PutLogEvents": {
        "logGroupName": "/aws/reference-example",
        "logStreamName": "application",
        "logEvents": [
          {
            "timestamp": 0,
            "message": "Static template: replace timestamp with current epoch milliseconds."
          }
        ]
      },
      "GetLogEvents": {
        "logGroupName": "/aws/reference-example",
        "logStreamName": "application",
        "startFromHead": true,
        "limit": 100
      }
    }
  },
  "kms": {
    "title": "KMS",
    "titleJA": "KMS",
    "protocol": "1.1",
    "target": "TrentService",
    "operations": [
      "CreateKey",
      "DescribeKey",
      "ListKeys",
      "EnableKey",
      "DisableKey",
      "GetKeyPolicy",
      "Encrypt",
      "Decrypt",
      "CreateAlias",
      "ListAliases",
      "UpdateAlias",
      "DeleteAlias"
    ],
    "templates": {
      "CreateKey": {
        "Description": "Local dummy key"
      },
      "DescribeKey": {
        "KeyId": "11111111-1111-4111-8111-111111111111"
      },
      "ListKeys": {
        "Limit": 1
      },
      "EnableKey": {
        "KeyId": "11111111-1111-4111-8111-111111111111"
      },
      "DisableKey": {
        "KeyId": "11111111-1111-4111-8111-111111111111"
      },
      "GetKeyPolicy": {
        "KeyId": "11111111-1111-4111-8111-111111111111",
        "PolicyName": "default"
      },
      "Encrypt": {
        "KeyId": "11111111-1111-4111-8111-111111111111",
        "Plaintext": "bG9jYWwgZHVtbXk=",
        "EncryptionContext": {
          "purpose": "demo"
        }
      },
      "Decrypt": {
        "CiphertextBlob": "VEhJU19JU19BX0RPQ1VNRU5UQVRJT05fUExBQ0VIT0xERVI=",
        "EncryptionContext": {
          "purpose": "demo"
        }
      },
      "CreateAlias": {
        "AliasName": "alias/practice",
        "TargetKeyId": "11111111-1111-4111-8111-111111111111"
      },
      "ListAliases": {
        "Limit": 1
      },
      "UpdateAlias": {
        "AliasName": "alias/practice",
        "TargetKeyId": "22222222-2222-4222-8222-222222222222"
      },
      "DeleteAlias": {
        "AliasName": "alias/practice"
      }
    }
  },
  "secretsmanager": {
    "title": "Secrets Manager",
    "titleJA": "Secrets Manager",
    "protocol": "1.1",
    "target": "secretsmanager",
    "operations": [
      "CreateSecret",
      "DescribeSecret",
      "ListSecrets",
      "ListSecretVersionIds",
      "PutSecretValue",
      "GetSecretValue",
      "UpdateSecretVersionStage",
      "DeleteSecret"
    ],
    "templates": {
      "CreateSecret": {
        "Name": "training/example",
        "Description": "Dummy local practice value",
        "ClientRequestToken": "00000000-0000-0000-0000-000000000001",
        "SecretString": "DUMMY-secret-example"
      },
      "DescribeSecret": {
        "SecretId": "arn:aws:secretsmanager:us-east-1:000000000000:secret:training/example-ABC123"
      },
      "ListSecrets": {
        "MaxResults": 10,
        "SortOrder": "asc"
      },
      "ListSecretVersionIds": {
        "SecretId": "training/version-list",
        "IncludeDeprecated": true,
        "MaxResults": 2
      },
      "PutSecretValue": {
        "SecretId": "arn:aws:secretsmanager:us-east-1:000000000000:secret:training/example-ABC123",
        "ClientRequestToken": "00000000-0000-0000-0000-000000000002",
        "SecretString": "DUMMY-secret-example-updated",
        "VersionStages": [
          "AWSCURRENT"
        ]
      },
      "GetSecretValue": {
        "SecretId": "arn:aws:secretsmanager:us-east-1:000000000000:secret:training/example-ABC123",
        "VersionStage": "AWSCURRENT"
      },
      "UpdateSecretVersionStage": {
        "SecretId": "arn:aws:secretsmanager:us-east-1:000000000000:secret:training/example-ABC123",
        "VersionStage": "AWSCURRENT",
        "MoveToVersionId": "00000000-0000-0000-0000-000000000001",
        "RemoveFromVersionId": "00000000-0000-0000-0000-000000000002"
      },
      "DeleteSecret": {
        "SecretId": "arn:aws:secretsmanager:us-east-1:000000000000:secret:training/example-ABC123",
        "ForceDeleteWithoutRecovery": true
      }
    }
  },
  "dynamodb": {
    "title": "DynamoDB",
    "titleJA": "DynamoDB",
    "protocol": "1.0",
    "target": "DynamoDB_20120810",
    "operations": [
      "CreateTable",
      "ListTables",
      "DescribeTable",
      "DeleteTable",
      "PutItem",
      "GetItem",
      "DeleteItem"
    ],
    "templates": {
      "CreateTable": {
        "TableName": "ddb-reference",
        "AttributeDefinitions": [
          {
            "AttributeName": "id",
            "AttributeType": "S"
          }
        ],
        "KeySchema": [
          {
            "AttributeName": "id",
            "KeyType": "HASH"
          }
        ],
        "BillingMode": "PAY_PER_REQUEST"
      },
      "ListTables": {
        "Limit": 10
      },
      "DescribeTable": {
        "TableName": "ddb-reference"
      },
      "DeleteTable": {
        "TableName": "ddb-reference"
      },
      "PutItem": {
        "TableName": "ddb-reference",
        "Item": {
          "id": {
            "S": "documented"
          },
          "payload": {
            "S": "sample <value> 日本語"
          }
        },
        "ConditionExpression": "attribute_not_exists(id)"
      },
      "GetItem": {
        "TableName": "ddb-reference",
        "Key": {
          "id": {
            "S": "documented"
          }
        },
        "ConsistentRead": true
      },
      "DeleteItem": {
        "TableName": "ddb-reference",
        "Key": {
          "id": {
            "S": "documented"
          }
        }
      }
    }
  },
  "cloudformation": {
    "title": "CloudFormation",
    "titleJA": "CloudFormation",
    "protocol": "query",
    "target": "2010-05-15",
    "operations": [
      "CreateStack",
      "DescribeStacks",
      "DeleteStack"
    ],
    "templates": {
      "CreateStack": {
        "Action": "CreateStack",
        "Version": "2010-05-15",
        "StackName": "reference-practice",
        "TemplateBody": "{\"Resources\": {\"Practice\": {\"Type\": \"AWS::SSM::Parameter\", \"Properties\": {\"Name\": \"/reference/stack\", \"Type\": \"String\", \"Value\": \"dummy\"}}}}"
      },
      "DescribeStacks": {
        "Action": "DescribeStacks",
        "Version": "2010-05-15"
      },
      "DeleteStack": {
        "Action": "DeleteStack",
        "Version": "2010-05-15",
        "StackName": "reference-practice"
      }
    }
  },
  "iam": {
    "title": "IAM",
    "titleJA": "IAM",
    "protocol": "query",
    "target": "2010-05-08",
    "operations": [
      "CreateRole",
      "GetRole",
      "ListRoles",
      "DeleteRole",
      "PutRolePolicy",
      "GetRolePolicy",
      "DeleteRolePolicy",
      "ListRolePolicies",
      "UpdateAssumeRolePolicy",
      "CreatePolicy",
      "GetPolicy",
      "ListPolicies",
      "DeletePolicy",
      "CreatePolicyVersion",
      "GetPolicyVersion",
      "ListPolicyVersions",
      "SetDefaultPolicyVersion",
      "DeletePolicyVersion",
      "AttachRolePolicy",
      "DetachRolePolicy",
      "ListAttachedRolePolicies"
    ],
    "templates": {
      "CreateRole": {
        "Action": "CreateRole",
        "Version": "2010-05-08",
        "RoleName": "practice-executor",
        "AssumeRolePolicyDocument": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"sts:AssumeRole\",\"Principal\":{\"Service\":\"lambda.amazonaws.com\"}}]}"
      },
      "GetRole": {
        "Action": "GetRole",
        "Version": "2010-05-08",
        "RoleName": "practice-executor"
      },
      "ListRoles": {
        "Action": "ListRoles",
        "Version": "2010-05-08",
        "MaxItems": 100
      },
      "DeleteRole": {
        "Action": "DeleteRole",
        "Version": "2010-05-08",
        "RoleName": "practice-executor"
      },
      "PutRolePolicy": {
        "Action": "PutRolePolicy",
        "Version": "2010-05-08",
        "RoleName": "practice-executor",
        "PolicyName": "ReadObjects",
        "PolicyDocument": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:GetObject\",\"Resource\":\"arn:aws:s3:::practice-bucket/*\"}]}"
      },
      "GetRolePolicy": {
        "Action": "GetRolePolicy",
        "Version": "2010-05-08",
        "RoleName": "practice-executor",
        "PolicyName": "ReadObjects"
      },
      "DeleteRolePolicy": {
        "Action": "DeleteRolePolicy",
        "Version": "2010-05-08",
        "RoleName": "practice-executor",
        "PolicyName": "ReadObjects"
      },
      "ListRolePolicies": {
        "Action": "ListRolePolicies",
        "Version": "2010-05-08",
        "RoleName": "practice-executor",
        "MaxItems": 100
      },
      "UpdateAssumeRolePolicy": {
        "Action": "UpdateAssumeRolePolicy",
        "Version": "2010-05-08",
        "RoleName": "practice-executor",
        "PolicyDocument": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"sts:AssumeRole\",\"Principal\":{\"Service\":\"lambda.amazonaws.com\"}}]}"
      },
      "CreatePolicy": {
        "Action": "CreatePolicy",
        "Version": "2010-05-08",
        "PolicyName": "ReadObjects",
        "PolicyDocument": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:GetObject\",\"Resource\":\"arn:aws:s3:::practice-bucket/*\"}]}"
      },
      "GetPolicy": {
        "Action": "GetPolicy",
        "Version": "2010-05-08",
        "PolicyArn": "arn:aws:iam::000000000000:policy/ReadObjects"
      },
      "ListPolicies": {
        "Action": "ListPolicies",
        "Version": "2010-05-08",
        "Scope": "Local",
        "MaxItems": 100
      },
      "DeletePolicy": {
        "Action": "DeletePolicy",
        "Version": "2010-05-08",
        "PolicyArn": "arn:aws:iam::000000000000:policy/ReadObjects"
      },
      "CreatePolicyVersion": {
        "Action": "CreatePolicyVersion",
        "Version": "2010-05-08",
        "PolicyArn": "arn:aws:iam::000000000000:policy/ReadObjects",
        "PolicyDocument": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:GetObject\",\"Resource\":\"arn:aws:s3:::practice-bucket/*\"}]}",
        "SetAsDefault": true
      },
      "GetPolicyVersion": {
        "Action": "GetPolicyVersion",
        "Version": "2010-05-08",
        "PolicyArn": "arn:aws:iam::000000000000:policy/ReadObjects",
        "VersionId": "v2"
      },
      "ListPolicyVersions": {
        "Action": "ListPolicyVersions",
        "Version": "2010-05-08",
        "PolicyArn": "arn:aws:iam::000000000000:policy/ReadObjects",
        "MaxItems": 100
      },
      "SetDefaultPolicyVersion": {
        "Action": "SetDefaultPolicyVersion",
        "Version": "2010-05-08",
        "PolicyArn": "arn:aws:iam::000000000000:policy/ReadObjects",
        "VersionId": "v2"
      },
      "DeletePolicyVersion": {
        "Action": "DeletePolicyVersion",
        "Version": "2010-05-08",
        "PolicyArn": "arn:aws:iam::000000000000:policy/ReadObjects",
        "VersionId": "v2"
      },
      "AttachRolePolicy": {
        "Action": "AttachRolePolicy",
        "Version": "2010-05-08",
        "RoleName": "practice-executor",
        "PolicyArn": "arn:aws:iam::000000000000:policy/ReadObjects"
      },
      "DetachRolePolicy": {
        "Action": "DetachRolePolicy",
        "Version": "2010-05-08",
        "RoleName": "practice-executor",
        "PolicyArn": "arn:aws:iam::000000000000:policy/ReadObjects"
      },
      "ListAttachedRolePolicies": {
        "Action": "ListAttachedRolePolicies",
        "Version": "2010-05-08",
        "RoleName": "practice-executor",
        "MaxItems": 100
      }
    }
  },
  "sns": {
    "title": "SNS",
    "titleJA": "SNS",
    "protocol": "query",
    "target": "2010-03-31",
    "operations": [
      "CreateTopic",
      "ListTopics",
      "DeleteTopic",
      "GetTopicAttributes",
      "Subscribe",
      "Unsubscribe",
      "ListSubscriptions",
      "ListSubscriptionsByTopic",
      "Publish"
    ],
    "templates": {
      "CreateTopic": {
        "Action": "CreateTopic",
        "Version": "2010-03-31",
        "Name": "reference-practice"
      },
      "ListTopics": {
        "Action": "ListTopics",
        "Version": "2010-03-31"
      },
      "DeleteTopic": {
        "Action": "DeleteTopic",
        "Version": "2010-03-31",
        "TopicArn": "arn:aws:sns:us-east-1:000000000000:reference-practice"
      },
      "GetTopicAttributes": {
        "Action": "GetTopicAttributes",
        "Version": "2010-03-31",
        "TopicArn": "arn:aws:sns:us-east-1:000000000000:reference-practice"
      },
      "Subscribe": {
        "Action": "Subscribe",
        "Version": "2010-03-31",
        "TopicArn": "arn:aws:sns:us-east-1:000000000000:reference-practice",
        "Protocol": "email",
        "Endpoint": "practice@example.invalid",
        "ReturnSubscriptionArn": "true"
      },
      "Unsubscribe": {
        "Action": "Unsubscribe",
        "Version": "2010-03-31",
        "SubscriptionArn": "arn:aws:sns:us-east-1:000000000000:reference-practice:REPLACE_WITH_ACTUAL_SUBSCRIPTION_ID"
      },
      "ListSubscriptions": {
        "Action": "ListSubscriptions",
        "Version": "2010-03-31"
      },
      "ListSubscriptionsByTopic": {
        "Action": "ListSubscriptionsByTopic",
        "Version": "2010-03-31",
        "TopicArn": "arn:aws:sns:us-east-1:000000000000:reference-practice"
      },
      "Publish": {
        "Action": "Publish",
        "Version": "2010-03-31",
        "TopicArn": "arn:aws:sns:us-east-1:000000000000:reference-practice",
        "Message": "dummy practice message"
      }
    }
  }
});
export const LOCALE_KEY = 'tenkacloud-simulator-locale';
export const MAX_BYTES = 262144;
export function inventory(capabilities) {
 const entries=[], failures=[];
 for(const [service,contract] of Object.entries(NATIVE)) {
  const declared=capabilities?.services?.[service]?.operations;
  if(!Array.isArray(declared)){failures.push(service+': MISSING_OPERATIONS');continue;}
  const names=new Set(declared);
  if(names.size!==declared.length) failures.push(service+': DUPLICATE_OPERATIONS');
  for(const operation of new Set([...contract.operations,...declared])) {
   const template=contract.templates[operation];
   const blocked=!names.has(operation)||!contract.operations.includes(operation)||!template;
   if(blocked) failures.push(service+'.'+operation+': UNMAPPED_OPERATION');
   entries.push({service,operation,title:contract.title,titleJA:contract.titleJA,protocol:contract.protocol,target:contract.target,template,blocked});
  }
 }
 for(const [service,value] of Object.entries(capabilities?.services||{})) {
  if(NATIVE[service]||['catalog','s3','lambda','apigateway'].includes(service))continue;
  failures.push(service+': UNKNOWN_SERVICE');
  for(const operation of Array.isArray(value?.operations)?value.operations:['(missing operation inventory)'])entries.push({service,operation,title:service,titleJA:service,blocked:true});
 }
 for(const service of capabilities?.services?.catalog?.services||[]) {
  if(service.protocol==='rest-json')continue; // Explicit dedicated native REST reference.
  if(service.protocol!=='json'){failures.push(service.model+': UNKNOWN_CATALOG_PROTOCOL');for(const operation of service.operations||['(missing operation inventory)'])entries.push({service:'catalog:'+service.model,operation,title:service.title||service.model,titleJA:service.title||service.model,blocked:true});continue;}
  const descriptors=[...(service.resources||[]).flatMap(r=>Object.values(r.operations||{})),...Object.values(service.tagging||{})];
  if(!Array.isArray(service.operations)||new Set(service.operations).size!==service.operations.length){failures.push(service.model+': INVALID_OPERATION_INVENTORY');continue;}
  for(const d of descriptors)if(!service.operations.includes(d.name))failures.push(service.model+'.'+d.name+': UNDECLARED_DESCRIPTOR');
  for(const operation of service.operations||[]) {
   const matching=descriptors.filter(d=>d.name===operation);
   const op=matching[0];
   const blocked=!op||matching.some(d=>d.method!=='POST'||d.uri!=='/')||!['1.0','1.1'].includes(service.jsonVersion)||!service.targetPrefix||!service.signingName;
   if(blocked)failures.push(service.model+'.'+operation+': MISSING_EXECUTABLE_DESCRIPTOR');
   const template={};
   // Required primitive placeholders are only an editable static draft, never
   // claimed to be a valid resource or successful native response.
   for(const m of op?.members||[]) if(m.required)template[m.locationName||m.name]=m.enum?.[0]??(m.type==='boolean'?false:['integer','long'].includes(m.type)?1:m.type==='list'?[]:m.type==='structure'||m.type==='map'?{}:'REPLACE_WITH_PRACTICE_VALUE');
   entries.push({service:'catalog:'+service.model,selector:service.signingName,operation,title:service.title,titleJA:service.title,protocol:service.jsonVersion,target:service.targetPrefix,template,blocked});
  }
 }
 return {entries,failures,dedicated:Object.keys(capabilities?.services||{}).filter(s=>['s3','lambda','apigateway'].includes(s)),otherProviders:(capabilities?.providers||[]).filter(p=>p!=='aws')};
}
export function draft(entry,now=Date.now()) {
 if(entry.blocked||!entry.template)throw Error('COVERAGE');
 const value=structuredClone(entry.template);
 if(entry.service==='logs'&&entry.operation==='PutLogEvents')for(const e of value.logEvents||[])e.timestamp=now;
 return JSON.stringify(value,null,2);
}
export function requestFor(entry,body,{region,principal,identitySelection,regions}) {
 if(entry.blocked||!entry.template)throw Error('COVERAGE');
 if(!regions.includes(region))throw Error('REGION');
 if(new TextEncoder().encode(body).length>MAX_BYTES)throw Error('SIZE');
 const value=JSON.parse(body);if(!value||typeof value!=='object'||Array.isArray(value))throw Error('BODY');
 const headers={'X-Simulator-Service':entry.selector||entry.service,'X-Simulator-Region':region};
 if(principal){if(identitySelection!==true)throw Error('IDENTITY');headers['X-Simulator-Principal']=principal;}
 let wire;
 if(entry.protocol==='query') {
  if(value.Action!==entry.operation||value.Version!==entry.target)throw Error('QUERY');
  if(Object.values(value).some(v=>v!==null&&typeof v==='object'))throw Error('QUERY');
  headers['Content-Type']='application/x-www-form-urlencoded';wire=new URLSearchParams(value).toString();
 }else{
  if(!['1.0','1.1'].includes(entry.protocol)||!entry.target)throw Error('COVERAGE');
  headers['Content-Type']='application/x-amz-json-'+entry.protocol;
  headers['X-Amz-Target']=entry.target+'.'+entry.operation;wire=JSON.stringify(value);
 }
 if(new TextEncoder().encode(wire).length>MAX_BYTES)throw Error('SIZE');
 return {path:'/',options:{method:'POST',credentials:'same-origin',redirect:'error',headers,body:wire}};
}
export async function boundedResponse(response,limit=MAX_BYTES) {
 const reader=response.body?.getReader();if(!reader)return '';
 let size=0;const chunks=[];
 try{for(;;){const {done,value}=await reader.read();if(done)break;size+=value.byteLength;if(size>limit)throw Error('SIZE');chunks.push(value);}}
 finally{await reader.cancel().catch(()=>{});reader.releaseLock();}
 const bytes=new Uint8Array(size);let offset=0;for(const c of chunks){bytes.set(c,offset);offset+=c.length;}return new TextDecoder().decode(bytes);
}

// Request-bound native values, never persisted. Receipt handles belong to the
// exact QueueUrl of the successful ReceiveMessage request, not the last queue.
export function recordSQS(cache,scope,operation,request,value) {
 const previous=cache.get(scope)||{queue:'',receipts:new Map()};
 if(['CreateQueue','GetQueueUrl'].includes(operation)&&typeof value.QueueUrl==='string')previous.queue=value.QueueUrl;
 const url=typeof request.QueueUrl==='string'?request.QueueUrl:'';
 if(operation==='ReceiveMessage'&&url){const handle=value.Messages?.[0]?.ReceiptHandle;if(typeof handle==='string')previous.receipts.set(url,handle);else previous.receipts.delete(url);}
 if(operation==='DeleteMessage'&&url&&previous.receipts.get(url)===request.ReceiptHandle)previous.receipts.delete(url);
 if(operation==='DeleteQueue'&&url){previous.receipts.delete(url);if(previous.queue===url)previous.queue='';}
 cache.set(scope,previous);
}
export function sqsValue(cache,scope,key,request) {
 const saved=cache.get(scope);if(!saved)return '';
 return key==='QueueUrl'?saved.queue:key==='ReceiptHandle'?saved.receipts.get(request.QueueUrl)||'':'';
}
