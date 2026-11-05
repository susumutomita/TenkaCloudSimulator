// Console discovery only: this catalog never grants API or exercise authority.
export const PROVIDERS = Object.freeze([
  {id: 'aws', label: 'AWS', defaultService: 's3'},
  {id: 'gcp', label: 'Google Cloud', defaultService: 'gcp-storage'},
  {id: 'azure', label: 'Azure', defaultService: 'azure-blob'},
]);
export const SERVICES = Object.freeze([
  {id: 's3', provider: 'aws', label: 'S3', capability: 's3', route: '/s3', keywords: 'bucket object バケット オブジェクト ストレージ'},
  {id: 'ssm', provider: 'aws', label: 'Systems Manager', capability: 'ssm', route: '/parameters', keywords: 'parameter store パラメータ 設定 システムマネージャー パラメータストア'},
  {id: 'lambda', provider: 'aws', label: 'Lambda', capability: 'lambda', route: '/lambda', keywords: 'function 関数 ラムダ'},
  {id: 'iam', provider: 'aws', label: 'IAM', capability: 'iam', route: '/iam', keywords: 'role identity ロール 権限 アイアム アクセス管理'},
  {id: 'apigateway', provider: 'aws', label: 'API Gateway', capability: 'apigateway', route: '/apigateway', keywords: 'rest api エーピーアイ APIゲートウェイ ゲートウェイ'},
  {id: 'cfn', provider: 'aws', label: 'CloudFormation', capability: 'cloudformation', route: '/stacks', keywords: 'stack template スタック テンプレート クラウドフォーメーション'},
  {id: 'kms', provider: 'aws', label: 'Key Management Service', capability: 'kms', route: '/kms', keywords: 'kms encryption key 暗号 キー 暗号化 キー管理'},
  {id: 'sqs', provider: 'aws', label: 'SQS', capability: 'sqs', route: '/sqs', keywords: 'queue message messaging キュー メッセージ メッセージング'},
  {id: 'logs', provider: 'aws', label: 'CloudWatch Logs', capability: 'logs', route: '/logs', keywords: 'log group stream monitoring ログ グループ ストリーム 監視'},
  {id: 'secretsmanager', provider: 'aws', label: 'Secrets Manager', capability: 'secretsmanager', route: '/secretsmanager', keywords: 'secret version credential シークレット バージョン 秘密'},
  {id: 'gcp-storage', provider: 'gcp', label: 'Cloud Storage', route: '/gcp-storage', keywords: 'google bucket object バケット オブジェクト ストレージ クラウドストレージ'},
  {id: 'azure-blob', provider: 'azure', label: 'Blob Storage', route: '/azure-blob', keywords: 'container blob コンテナー コンテナ ブロブ ストレージ ブロブストレージ'},
]);
export function serviceDescriptor(id) { return SERVICES.find(service => service.id === id); }
export function providerForService(id) { return serviceDescriptor(id)?.provider ?? null; }
export function serviceAvailable(id, capabilities) {
  const service = serviceDescriptor(id);
  if (!service || !capabilities) return false;
  if (service.provider === 'aws') return !!capabilities.services?.[service.capability];
  const scope = capabilities.cloudStorage?.[service.provider];
  return service.provider === 'gcp' ? typeof scope?.project === 'string' && scope.project.length > 0
    : typeof scope?.account === 'string' && scope.account.length > 0;
}
export function servicesForProvider(provider, capabilities, query = '') {
  const search = String(query).normalize('NFKC').trim().toLocaleLowerCase('en');
  return SERVICES.filter(service => service.provider === provider && serviceAvailable(service.id, capabilities)
    && (service.label + ' ' + service.keywords).normalize('NFKC').toLocaleLowerCase('en').includes(search));
}
export function serviceFromHash(hash) {
  if (!hash || hash === '#' || hash === '#/') return 's3';
  const first = /^#\/([^/]+)(?:\/|$)/.exec(hash)?.[1];
  const aliases = {parameters: 'ssm', stacks: 'cfn', functions: 'lambda', roles: 'iam'};
  const id = aliases[first] ?? first;
  return serviceDescriptor(id) ? id : null;
}
export function hashForService(id) { return serviceDescriptor(id)?.route ?? null; }
