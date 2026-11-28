import {scalingPlansAvailable} from './autoscaling-plans.mjs';
import {sakuraServerEndpoint} from './sakura-server-read.mjs';
import {supportAdvertised} from './support-client.mjs';
import {route53ConsoleAdvertised} from './route53-console.mjs';
import {traceConfiguration} from './gcp-trace.mjs';

// Console discovery only: this catalog never grants API or exercise authority.
export const PROVIDERS = Object.freeze([
  {id: 'aws', label: 'AWS', defaultService: 'home'},
  {id: 'gcp', label: 'Google Cloud', defaultService: 'gcp-storage'},
  {id: 'azure', label: 'Azure', defaultService: 'azure-blob'},
  {id: 'sakura', label: 'Sakura Cloud', defaultService: 'sakura-disks'},
]);
export const SERVICES = Object.freeze([
  {id: 'ec2', provider: 'aws', label: 'EC2', capability: 'ec2', route: '/ec2', keywords: 'instance compute インスタンス コンピューティング'},
  {id: 'vpc', provider: 'aws', label: 'VPC', capability: 'ec2', route: '/vpc', keywords: 'network virtual private cloud ネットワーク 仮想プライベートクラウド'},
  {id: 's3', provider: 'aws', label: 'S3', capability: 's3', route: '/s3', keywords: 'bucket object バケット オブジェクト ストレージ'},
  {id: 'ssm', provider: 'aws', label: 'Systems Manager', capability: 'ssm', route: '/parameters', keywords: 'parameter store パラメータ 設定 システムマネージャー パラメータストア'},
  {id: 'ssm-documents', provider: 'aws', label: 'SSM Documents', capability: 'ssm', route: '/ssm-documents', keywords: 'systems manager documents versions compare ドキュメント バージョン'},
  {id: 'ssm-session', provider: 'aws', label: 'Session Manager', capability: 'ssm', route: '/session-manager', keywords: 'systems manager session shell terminal connect セッションマネージャー セッション ターミナル 接続 システムマネージャー'},
  {id: 'lambda', provider: 'aws', label: 'Lambda', capability: 'lambda', route: '/lambda', keywords: 'function 関数 ラムダ'},
  {id: 'iam', provider: 'aws', label: 'IAM', capability: 'iam', route: '/iam', keywords: 'role identity ロール 権限 アイアム アクセス管理'},
  {id: 'wafv2', provider: 'aws', label: 'AWS WAF', capability: 'wafv2', route: '/wafv2', keywords: 'web acl firewall rules ウェブACL ファイアウォール ルール'},
  {id: 'apigateway', provider: 'aws', label: 'API Gateway', capability: 'apigateway', route: '/apigateway', keywords: 'rest api エーピーアイ APIゲートウェイ ゲートウェイ'},
  {id: 'cfn', provider: 'aws', label: 'CloudFormation', capability: 'cloudformation', route: '/stacks', keywords: 'stack template スタック テンプレート クラウドフォーメーション'},
  {id: 'kms', provider: 'aws', label: 'Key Management Service', capability: 'kms', route: '/kms', keywords: 'kms encryption key 暗号 キー 暗号化 キー管理'},
  {id: 'dynamodb', provider: 'aws', label: 'DynamoDB', capability: 'dynamodb', route: '/dynamodb', keywords: 'table item key-value database ddb ダイナモ ダイナモDB テーブル 項目 キー データベース'},
  {id: 'autoscaling-plans', provider: 'aws', label: 'AWS Auto Scaling Plans', capability: 'autoscaling-plans', route: '/autoscaling-plans', keywords: 'scaling plans capacity dynamodb スケーリングプラン テーブル 容量'},
  {id: 'route53', provider: 'aws', label: 'Route 53', capability: 'route53', route: '/route53', keywords: 'dns hosted zones records ptr reverse lookup 公開ホストゾーン 逆引き レコード'},
  {id: 'support', provider: 'aws', label: 'AWS Support', capability: 'support', route: '/support', keywords: 'support cases communication attachments サポート ケース メッセージ 添付ファイル'},
  {id: 'simpledb', provider: 'aws', label: 'SimpleDB', capability: 'simpledb', route: '/simpledb', keywords: 'database domain item attributes conditional データベース ドメイン 項目 属性'},
  {id: 'sns', provider: 'aws', label: 'SNS', capability: 'sns', route: '/sns', keywords: 'simple notification service subscription topic サブスクリプション トピック 通知'},
  {id: 'sqs', provider: 'aws', label: 'SQS', capability: 'sqs', route: '/sqs', keywords: 'queue message messaging キュー メッセージ メッセージング シンプルキューサービス'},
  {id: 'logs', provider: 'aws', label: 'CloudWatch Logs', capability: 'logs', route: '/logs', keywords: 'log group stream monitoring ログ グループ ストリーム 監視'},
  {id: 'secretsmanager', provider: 'aws', label: 'Secrets Manager', capability: 'secretsmanager', route: '/secretsmanager', keywords: 'secret version credential credentials シークレット バージョン 秘密 認証情報'},
  {id: 'catalog', provider: 'aws', label: 'Service catalog', capability: 'catalog', route: '/catalog', keywords: 'catalog metadata control plane カタログ サービス メタデータ'},
  {id: 'gcp-storage', provider: 'gcp', label: 'Cloud Storage', route: '/gcp-storage', keywords: 'google bucket object バケット オブジェクト ストレージ クラウドストレージ'},
  {id: 'gcp-catalog', provider: 'gcp', label: 'Service catalog', route: '/gcp-catalog', keywords: 'google catalog api metadata カタログ サービス'},
  {id: 'gcp-dns', provider: 'gcp', label: 'Cloud DNS', route: '/gcp-dns', keywords: 'google cloud dns managed zones records ゾーン レコード'},
  {id: 'gcp-trace', provider: 'gcp', label: 'Trace', route: '/gcp-trace', keywords: 'google cloud trace explorer span observations observability トレース スパン 観測'},
  {id: 'gcp-appengine', provider: 'gcp', label: 'App Engine', route: '/gcp-appengine', keywords: 'google app engine appengine gae application version traffic split firewall python wsgi アプリ アプリケーション バージョン トラフィック ファイアウォール'},
  {id: 'azure-queue', provider: 'azure', label: 'Queue Storage', route: '/azure-queue', keywords: 'azure storage queue message キュー ストレージ メッセージ'},
  {id: 'azure-blob', provider: 'azure', label: 'Blob Storage', route: '/azure-blob', keywords: 'container blob コンテナー コンテナ ブロブ ストレージ ブロブストレージ'},
  {id: 'azure-arm', provider: 'azure', label: 'Resource Manager', route: '/azure-arm', keywords: 'arm resource group subscription リソース グループ リソースグループ サブスクリプション リソースマネージャー'},
  {id: 'sakura-servers', provider: 'sakura', label: 'Servers', route: '/sakura-servers', keywords: 'sakura cloud server metadata さくらのクラウド サーバ メタデータ'},
  {id: 'sakura-disks', provider: 'sakura', label: 'Disks', route: '/sakura-disks', keywords: 'sakura cloud disk configuration storage さくらのクラウド さくら ディスク 構成 ストレージ'},
]);
export function serviceDescriptor(id) { return SERVICES.find(service => service.id === id); }
export function providerForService(id) { return id === 'home' ? 'aws' : serviceDescriptor(id)?.provider ?? null; }
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
const dnsOperations = ['managedZones.list', 'managedZones.create', 'managedZones.get', 'resourceRecordSets.list'];
function dnsConfigured(cap) {
  return cap?.console === true && cap.api === 'dns.v1'
    && typeof cap.project === 'string' && /^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(cap.project)
    && cap.endpoint === '/_simulator/providers/gcp/dns/v1/projects/' + cap.project + '/managedZones'
    && Array.isArray(cap.operations) && cap.operations.length === dnsOperations.length + (cap.operations.includes('changes.create') ? 1 : 0)
    && dnsOperations.every(op => cap.operations.includes(op));
}
export function dnsCapabilitiesForPrincipal(session, epoch, principal, discovered) {
  if (!session || epoch === 0) return session;
  return {...session, gcpDns: discovered?.epoch === epoch && discovered.principal === principal ? discovered.value : undefined};
}
const queueOperations = ['CreateQueue', 'DeleteQueue', 'PutMessage', 'GetMessages', 'PeekMessages', 'UpdateMessage', 'DeleteMessage', 'ClearMessages'];
function queueConfigured(scope) {
  return scope?.console === true && scope.apiVersion === '2023-11-03'
    && typeof scope.account === 'string' && /^[a-z0-9]{3,24}$/.test(scope.account)
    && scope.endpoint === '/_simulator/providers/azure-queue/' + scope.account
    && Array.isArray(scope.operations) && queueOperations.every(op => scope.operations.includes(op));
}
export function queueCapabilitiesForPrincipal(session, epoch, principal, discovered) {
  if (!session || epoch === 0) return session;
  const queue = discovered?.epoch === epoch && discovered.principal === principal ? discovered.queue : undefined;
  return {...session, azureQueue: queue};
}
export function traceCapabilitiesForPrincipal(session, epoch, principal, discovered) {
  if (!session || epoch === 0) return session;
  return {...session, gcpTrace: discovered?.epoch === epoch && discovered.principal === principal ? discovered.trace : undefined};
}
export function sakuraServerConfiguration(capabilities) {
  const scope = capabilities?.sakuraServer;
  if (!Array.isArray(capabilities?.providers) || !capabilities.providers.includes('sakura')
      || capabilities.consoleScope || scope?.console !== true || scope.availabilityReported !== false
      || !sakuraServerEndpoint(scope)) return null;
  return scope;
}
export function serviceAvailable(id, capabilities) {
  if (id === 'home') return !!capabilities;
  const service = serviceDescriptor(id);
  if (!service || !capabilities) return false;
  // A participant handoff exposes only its bound target: one parameter, or one
  // instance (EC2 and Session Manager). This presentation restriction never
  // replaces the server's exact-resource authorization.
  const handoff = capabilities.consoleScope;
  if (id === 'ssm-session') return typeof handoff?.instanceId === 'string' && !!capabilities.services?.ssm;
  if (handoff && (typeof handoff.instanceId === 'string' ? id !== 'ec2' : id !== 'ssm')) return false;
  if (id === 'ec2' || id === 'vpc') {
    const ec2=capabilities.services?.ec2;
    return ec2?.console === true && Array.isArray(ec2.operations) && ['DescribeInstances','DescribeVpcs'].every(op=>ec2.operations.includes(op));
  }
  if (id === 'autoscaling-plans') return scalingPlansAvailable(capabilities);
  if (id === 'route53') return route53ConsoleAdvertised(capabilities);
  if (id === 'support') return supportAdvertised(capabilities);
  if (id === 'simpledb') {
    const scope = capabilities.services?.simpledb;
    return scope?.console === true && scope.region === 'us-east-1' && scope.conditionalPut === true
      && Array.isArray(scope.operations)
      && ['ListDomains', 'CreateDomain', 'GetAttributes', 'PutAttributes'].every(op => scope.operations.includes(op));
  }
  if (id === 'sns') {
    const scope = capabilities.services?.sns;
    return scope?.console === true && Array.isArray(scope.operations)
      && ['ListTopics', 'ListSubscriptionsByTopic', 'Subscribe', 'Unsubscribe'].every(op => scope.operations.includes(op));
  }
  if (id === 'ssm-documents') {
    const documents = capabilities.services?.ssm?.documentManagement;
    return documents?.definitionOnly === true && documents.execution === false
      && Array.isArray(documents.operations)
      && ['ListDocuments', 'ListDocumentVersions', 'GetDocument'].every(op => documents.operations.includes(op));
  }
  if (service.provider === 'aws') return !!capabilities.services?.[service.capability];
  if (id === 'sakura-servers') return sakuraServerConfiguration(capabilities) !== null;
  if (service.provider === 'sakura') return sakuraConfiguration(capabilities) !== null;
  if (service.id === 'gcp-appengine') return typeof capabilities.gcpAppEngine?.endpoint === 'string' && typeof capabilities.gcpAppEngine.project === 'string'
    && capabilities.gcpAppEngine.project.length > 0 && typeof capabilities.gcpAppEngine.dataPlane === 'string';
  if (service.id === 'gcp-dns') return dnsConfigured(capabilities.gcpDns);
  if (service.id === 'gcp-trace') return traceConfiguration(capabilities.gcpTrace) !== null;
  if (service.id === 'gcp-catalog') return Array.isArray(capabilities.gcpCatalog?.apis) && capabilities.gcpCatalog.apis.length > 0;
  if (service.id === 'azure-queue') return queueConfigured(capabilities.azureQueue);
  if (service.id === 'azure-arm') return Array.isArray(capabilities.azureCatalog?.providers) && capabilities.azureCatalog.providers.length > 0
    && typeof capabilities.azureCatalog.descriptors === 'string';
  const scope = capabilities.cloudStorage?.[service.provider];
  return service.provider === 'gcp' ? typeof scope?.project === 'string' && scope.project.length > 0
    : typeof scope?.account === 'string' && scope.account.length > 0;
}
// The VPC create/delete controls need both native operations; a handoff scope
// never shows VPC at all. The server still authorizes every request.
export function vpcLifecycleAvailable(capabilities) {
  const operations = capabilities?.services?.ec2?.operations;
  return !capabilities?.consoleScope && Array.isArray(operations) && ['CreateVpc', 'DeleteVpc'].every(op => operations.includes(op));
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
  if (!hash || hash === '#' || hash === '#/') return 'home';
  const first = /^#\/([^/?]+)(?:[/?]|$)/.exec(hash)?.[1];
  const aliases = {'session-manager': 'ssm-session', secrets: 'secretsmanager', parameters: 'ssm', stacks: 'cfn', functions: 'lambda', roles: 'iam'};
  const id = aliases[first] ?? first;
  return id === 'home' || serviceDescriptor(id) ? id : null;
}
export function hashForService(id) { return id === 'home' ? '/home' : serviceDescriptor(id)?.route ?? null; }
