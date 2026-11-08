// Console discovery only: this catalog never grants API or exercise authority.
export const PROVIDERS = Object.freeze([
  {id: 'aws', label: 'AWS', defaultService: 's3'},
  {id: 'gcp', label: 'Google Cloud', defaultService: 'gcp-storage'},
  {id: 'azure', label: 'Azure', defaultService: 'azure-blob'},
  {id: 'sakura', label: 'Sakura Cloud', defaultService: 'sakura-disks'},
]);
export const SERVICES = Object.freeze([
  {id: 's3', provider: 'aws', label: 'S3', capability: 's3', route: '/s3', keywords: 'bucket object バケット オブジェクト ストレージ'},
  {id: 'ssm', provider: 'aws', label: 'Systems Manager', capability: 'ssm', route: '/parameters', keywords: 'parameter store パラメータ 設定 システムマネージャー パラメータストア'},
  {id: 'lambda', provider: 'aws', label: 'Lambda', capability: 'lambda', route: '/lambda', keywords: 'function 関数 ラムダ'},
  {id: 'iam', provider: 'aws', label: 'IAM', capability: 'iam', route: '/iam', keywords: 'role identity ロール 権限 アイアム アクセス管理'},
  {id: 'apigateway', provider: 'aws', label: 'API Gateway', capability: 'apigateway', route: '/apigateway', keywords: 'rest api エーピーアイ APIゲートウェイ ゲートウェイ'},
  {id: 'cfn', provider: 'aws', label: 'CloudFormation', capability: 'cloudformation', route: '/stacks', keywords: 'stack template スタック テンプレート クラウドフォーメーション'},
  {id: 'kms', provider: 'aws', label: 'Key Management Service', capability: 'kms', route: '/kms', keywords: 'kms encryption key 暗号 キー 暗号化 キー管理'},
  {id: 'dynamodb', provider: 'aws', label: 'DynamoDB', capability: 'dynamodb', route: '/dynamodb', keywords: 'table item key-value database ddb ダイナモ ダイナモDB テーブル 項目 キー データベース'},
  {id: 'sqs', provider: 'aws', label: 'SQS', capability: 'sqs', route: '/sqs', keywords: 'queue message messaging キュー メッセージ メッセージング シンプルキューサービス'},
  {id: 'logs', provider: 'aws', label: 'CloudWatch Logs', capability: 'logs', route: '/logs', keywords: 'log group stream monitoring ログ グループ ストリーム 監視'},
  {id: 'secretsmanager', provider: 'aws', label: 'Secrets Manager', capability: 'secretsmanager', route: '/secretsmanager', keywords: 'secret version credential credentials シークレット バージョン 秘密 認証情報'},
  {id: 'catalog', provider: 'aws', label: 'Service catalog', capability: 'catalog', route: '/catalog', keywords: 'catalog metadata control plane カタログ サービス メタデータ'},
  {id: 'gcp-storage', provider: 'gcp', label: 'Cloud Storage', route: '/gcp-storage', keywords: 'google bucket object バケット オブジェクト ストレージ クラウドストレージ'},
  {id: 'gcp-catalog', provider: 'gcp', label: 'Service catalog', route: '/gcp-catalog', keywords: 'google catalog api metadata カタログ サービス'},
  {id: 'azure-blob', provider: 'azure', label: 'Blob Storage', route: '/azure-blob', keywords: 'container blob コンテナー コンテナ ブロブ ストレージ ブロブストレージ'},
  {id: 'azure-arm', provider: 'azure', label: 'Resource Manager', route: '/azure-arm', keywords: 'arm resource group subscription リソース グループ リソースグループ サブスクリプション リソースマネージャー'},
  {id: 'sakura-disks', provider: 'sakura', label: 'Disks', route: '/sakura-disks', keywords: 'sakura cloud disk configuration storage さくらのクラウド さくら ディスク 構成 ストレージ'},
]);
export function serviceDescriptor(id) { return SERVICES.find(service => service.id === id); }
export function providerForService(id) { return serviceDescriptor(id)?.provider ?? null; }
// Discovery only; server-side trusted Actor/owner checks remain authoritative.
// No environment/project/owner is inferred from client headers or AWS regions.
export function sakuraConfiguration(capabilities) {
  const scope = capabilities?.sakura;
  if (!Array.isArray(capabilities?.providers) || !capabilities.providers.includes('sakura')
      || scope?.console !== true || scope.apiVersion !== '1.1'
      || scope.service !== 'IaaS Disk configuration' || scope.availabilityReported !== false
      || typeof scope.endpoint !== 'string') return null;
  const match = /^\/_simulator\/providers\/sakura\/cloud\/zone\/([a-z0-9][a-z0-9-]{0,31})\/api\/cloud\/1\.1\/disk$/.exec(scope.endpoint);
  const expected = ['GET /disk','POST /disk','GET /disk/{id}','PUT /disk/{id}','DELETE /disk/{id}'];
  if (!match || !Array.isArray(scope.operations) || scope.operations.length !== expected.length
      || new Set(scope.operations).size !== expected.length
      || !expected.every(operation => scope.operations.includes(operation))) return null;
  return {endpoint: scope.endpoint, zone: match[1]};
}
export function serviceAvailable(id, capabilities) {
  const service = serviceDescriptor(id);
  if (!service || !capabilities) return false;
  if (service.provider === 'aws') return !!capabilities.services?.[service.capability];
  if (service.provider === 'sakura') return sakuraConfiguration(capabilities) !== null;
  if (service.id === 'gcp-catalog') return Array.isArray(capabilities.gcpCatalog?.apis) && capabilities.gcpCatalog.apis.length > 0;
  if (service.id === 'azure-arm') return Array.isArray(capabilities.azureCatalog?.providers) && capabilities.azureCatalog.providers.length > 0
    && typeof capabilities.azureCatalog.descriptors === 'string';
  const scope = capabilities.cloudStorage?.[service.provider];
  return service.provider === 'gcp' ? typeof scope?.project === 'string' && scope.project.length > 0
    : typeof scope?.account === 'string' && scope.account.length > 0;
}
// Catalog service titles come from capabilities so search finds each one.
function searchText(service, capabilities) {
  const titles = service.id === 'catalog' && Array.isArray(capabilities?.services?.catalog?.services)
    ? capabilities.services.catalog.services.map(entry => entry.title + ' ' + entry.serviceId).join(' ')
    : service.id === 'gcp-catalog' && Array.isArray(capabilities?.gcpCatalog?.apis)
      ? capabilities.gcpCatalog.apis.map(entry => entry.title + ' ' + entry.name).join(' ')
    : service.id === 'azure-arm' && Array.isArray(capabilities?.azureCatalog?.providers)
      ? capabilities.azureCatalog.providers.map(entry => entry.title + ' ' + entry.namespace + ' '
        + (Array.isArray(entry.types) ? entry.types.map(type => type.title).join(' ') : '')).join(' ') : '';
  return (service.label + ' ' + service.keywords + ' ' + titles).normalize('NFKC').toLocaleLowerCase('en');
}
export function servicesForProvider(provider, capabilities, query = '') {
  const search = String(query).normalize('NFKC').trim().toLocaleLowerCase('en');
  return SERVICES.filter(service => service.provider === provider && serviceAvailable(service.id, capabilities)
    && searchText(service, capabilities).includes(search));
}
export function serviceFromHash(hash) {
  if (!hash || hash === '#' || hash === '#/') return 's3';
  const first = /^#\/([^/]+)(?:\/|$)/.exec(hash)?.[1];
  const aliases = {secrets: 'secretsmanager', parameters: 'ssm', stacks: 'cfn', functions: 'lambda', roles: 'iam'};
  const id = aliases[first] ?? first;
  return serviceDescriptor(id) ? id : null;
}
export function hashForService(id) { return serviceDescriptor(id)?.route ?? null; }
