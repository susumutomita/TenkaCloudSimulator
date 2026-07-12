import {
  CoreError,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
  type ResourceRecord,
} from '@tenkacloud/simulator-core';
import {
  resourcesForDeployment,
  result,
  stateObject,
  storedProperties,
} from './state';

const INSTANCE = 'AWS::EC2::Instance';
const VPC = 'AWS::EC2::VPC';
const SUBNET = 'AWS::EC2::Subnet';
const INTERNET_GATEWAY = 'AWS::EC2::InternetGateway';
const GATEWAY_ATTACHMENT = 'AWS::EC2::VPCGatewayAttachment';
const ROUTE = 'AWS::EC2::Route';
const ROUTE_TABLE = 'AWS::EC2::RouteTable';
const ROUTE_ASSOCIATION = 'AWS::EC2::SubnetRouteTableAssociation';
const SECURITY_GROUP = 'AWS::EC2::SecurityGroup';
const LOAD_BALANCER = 'AWS::ElasticLoadBalancingV2::LoadBalancer';
const LISTENER = 'AWS::ElasticLoadBalancingV2::Listener';
const TARGET_GROUP = 'AWS::ElasticLoadBalancingV2::TargetGroup';
const WEB_ACL = 'AWS::WAFv2::WebACL';
const WEB_ACL_ASSOCIATION = 'AWS::WAFv2::WebACLAssociation';

const INPUT_FIELDS = new Set([
  'SourceCidr',
  'IpProtocol',
  'Port',
  'DestinationInstanceId',
  'DestinationLoadBalancerArn',
]);

type TransportProtocol = 'tcp' | 'udp';
type PathDecision = 'allow' | 'deny' | 'not-associated';

interface Ipv4Cidr {
  readonly network: number;
  readonly prefix: number;
  readonly text: string;
}

interface ReachabilityRequest {
  readonly source: Ipv4Cidr;
  readonly protocol: TransportProtocol;
  readonly port: number;
  readonly destinationInstanceId?: string;
  readonly destinationLoadBalancerArn?: string;
}

interface ReachabilityPathStep {
  readonly resourceType: string;
  readonly resourceId: string;
  readonly decision: PathDecision;
}

interface Evaluation {
  readonly path: ReachabilityPathStep[];
  readonly reasons: string[];
}

interface InternetPath {
  readonly evaluation: Evaluation;
  readonly subnet: ResourceRecord;
  readonly vpcId: string;
}

interface SecurityGroupEvaluation {
  readonly allowed: boolean;
  readonly path: readonly ReachabilityPathStep[];
}

interface TargetEvaluation {
  readonly allowed: boolean;
  readonly instance: ResourceRecord;
  readonly path: readonly ReachabilityPathStep[];
  readonly reasons: readonly string[];
}

function validation(message: string): never {
  throw new CoreError('ValidationFailed', message);
}

function projection(message: string): never {
  throw new CoreError('Conflict', message);
}

function requiredText(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    validation(`${label} must not be empty`);
  }
  return value;
}

function projectedText(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    projection(`${label} is missing from the network projection`);
  }
  return value;
}

function projectedObject(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    projection(`${label} is invalid in the network projection`);
  }
  return Object.fromEntries(Object.entries(value));
}

function projectedArray(value: unknown, label: string): readonly unknown[] {
  if (!Array.isArray(value)) {
    projection(`${label} is invalid in the network projection`);
  }
  return value;
}

function ipv4Address(value: string, label: string, input: boolean): number {
  const parts = value.split('.');
  const invalid =
    parts.length !== 4 ||
    parts.some(
      (part) => !/^(?:0|[1-9][0-9]{0,2})$/.test(part) || Number(part) > 255
    );
  if (invalid) {
    if (input) validation(`${label} must be a canonical IPv4 CIDR`);
    projection(`${label} is invalid in the network projection`);
  }
  return parts.reduce(
    (address, part) => ((address << 8) | Number(part)) >>> 0,
    0
  );
}

function cidr(value: unknown, label: string, input: boolean): Ipv4Cidr {
  if (typeof value !== 'string') {
    if (input) validation(`${label} must be a canonical IPv4 CIDR`);
    projection(`${label} is invalid in the network projection`);
  }
  const match = /^([^/]+)\/(0|[1-9]|[12][0-9]|3[0-2])$/.exec(value);
  if (!match?.[1] || match[2] === undefined) {
    if (input) validation(`${label} must be a canonical IPv4 CIDR`);
    projection(`${label} is invalid in the network projection`);
  }
  const address = ipv4Address(match[1], label, input);
  const prefix = Number(match[2]);
  const mask = prefix === 0 ? 0 : (0xffffffff << (32 - prefix)) >>> 0;
  const network = (address & mask) >>> 0;
  if (network !== address) {
    if (input) validation(`${label} must be a canonical IPv4 CIDR`);
    projection(`${label} is not canonical in the network projection`);
  }
  return { network, prefix, text: value };
}

function contains(parent: Ipv4Cidr, child: Ipv4Cidr): boolean {
  if (parent.prefix > child.prefix) return false;
  const mask =
    parent.prefix === 0 ? 0 : (0xffffffff << (32 - parent.prefix)) >>> 0;
  return (child.network & mask) >>> 0 === parent.network;
}

function request(command: ProviderCommandInput): ReachabilityRequest {
  if (command.resourceType !== INSTANCE) {
    validation(`EvaluateReachability resourceType must be ${INSTANCE}`);
  }
  const unknown = Object.keys(command.input).find(
    (key) => !INPUT_FIELDS.has(key)
  );
  if (unknown) validation(`EvaluateReachability field ${unknown} is unknown`);
  const protocol = requiredText(command.input['IpProtocol'], 'IpProtocol');
  if (protocol !== 'tcp' && protocol !== 'udp') {
    validation('IpProtocol must be tcp or udp');
  }
  const port = command.input['Port'];
  if (
    typeof port !== 'number' ||
    !Number.isSafeInteger(port) ||
    port < 1 ||
    port > 65_535
  ) {
    validation('Port must be an integer between 1 and 65535');
  }
  const instance = command.input['DestinationInstanceId'];
  const loadBalancer = command.input['DestinationLoadBalancerArn'];
  if ((instance === undefined) === (loadBalancer === undefined)) {
    validation('exactly one destination instance or load balancer is required');
  }
  return {
    source: cidr(command.input['SourceCidr'], 'SourceCidr', true),
    protocol,
    port,
    ...(instance === undefined
      ? {}
      : {
          destinationInstanceId: requiredText(
            instance,
            'DestinationInstanceId'
          ),
        }),
    ...(loadBalancer === undefined
      ? {}
      : {
          destinationLoadBalancerArn: requiredText(
            loadBalancer,
            'DestinationLoadBalancerArn'
          ),
        }),
  };
}

function resources(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  resourceType: string
): readonly ResourceRecord[] {
  return resourcesForDeployment(world, command.deploymentId, resourceType);
}

function unique(
  candidates: readonly ResourceRecord[],
  label: string
): ResourceRecord {
  if (candidates.length === 0) {
    throw new CoreError('NotFound', `${label} does not exist`);
  }
  if (candidates.length !== 1) projection(`${label} is ambiguous`);
  const candidate = candidates[0];
  if (!candidate) projection(`${label} disappeared`);
  return candidate;
}

function byReference(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  resourceType: string,
  reference: string,
  label: string
): ResourceRecord {
  return unique(
    resources(world, command, resourceType).filter((resource) => {
      const stored = storedProperties(resource);
      return (
        stored.refValue === reference ||
        stored.physicalId === reference ||
        stored.attributes['Arn'] === reference ||
        stored.attributes['LoadBalancerArn'] === reference
      );
    }),
    label
  );
}

function step(
  resource: ResourceRecord,
  decision: PathDecision
): ReachabilityPathStep {
  return {
    resourceType: resource.resourceType,
    resourceId: resource.resourceId,
    decision,
  };
}

function addReason(evaluation: Evaluation, reason: string): void {
  if (!evaluation.reasons.includes(reason)) evaluation.reasons.push(reason);
}

function projectedStrings(value: unknown, label: string): readonly string[] {
  const entries = projectedArray(value, label).map((item, index) =>
    projectedText(item, `${label}[${index}]`)
  );
  if (entries.length === 0) projection(`${label} must not be empty`);
  if (new Set(entries).size !== entries.length) {
    projection(`${label} contains duplicate references`);
  }
  return [...entries].sort();
}

function template(resource: ResourceRecord): Readonly<Record<string, unknown>> {
  return storedProperties(resource).templateProperties;
}

function matching(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  resourceType: string,
  predicate: (properties: Readonly<Record<string, unknown>>) => boolean
): readonly ResourceRecord[] {
  return resources(world, command, resourceType).filter((resource) =>
    predicate(template(resource))
  );
}

function internetPath(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  subnetId: string,
  evaluation: Evaluation
): InternetPath {
  const subnet = byReference(
    world,
    command,
    SUBNET,
    subnetId,
    'destination subnet'
  );
  const vpcId = projectedText(template(subnet)['VpcId'], 'subnet VpcId');
  const vpc = byReference(world, command, VPC, vpcId, 'destination VPC');
  evaluation.path.push(step(vpc, 'allow'), step(subnet, 'allow'));

  const associations = matching(
    world,
    command,
    ROUTE_ASSOCIATION,
    (properties) => properties['SubnetId'] === subnetId
  );
  if (associations.length === 0) {
    addReason(evaluation, 'SUBNET_ROUTE_TABLE_UNASSOCIATED');
    return { evaluation, subnet, vpcId };
  }
  if (associations.length !== 1) {
    projection('route-table association is ambiguous');
  }
  const association = associations[0];
  if (!association) projection('route-table association disappeared');
  evaluation.path.push(step(association, 'allow'));
  const routeTableId = projectedText(
    template(association)['RouteTableId'],
    'association RouteTableId'
  );
  const routeTable = byReference(
    world,
    command,
    ROUTE_TABLE,
    routeTableId,
    'associated route table'
  );
  if (template(routeTable)['VpcId'] !== vpcId) {
    projection('associated route table belongs to a different VPC');
  }
  evaluation.path.push(step(routeTable, 'allow'));

  const routes = matching(world, command, ROUTE, (properties) => {
    return (
      properties['RouteTableId'] === routeTableId &&
      properties['DestinationCidrBlock'] === '0.0.0.0/0'
    );
  });
  if (routes.length === 0) {
    addReason(evaluation, 'DEFAULT_ROUTE_MISSING');
    return { evaluation, subnet, vpcId };
  }
  if (routes.length !== 1) projection('default route is ambiguous');
  const route = routes[0];
  if (!route) projection('default route disappeared');
  evaluation.path.push(step(route, 'allow'));
  const gatewayId = template(route)['GatewayId'];
  if (typeof gatewayId !== 'string' || !gatewayId) {
    addReason(evaluation, 'DEFAULT_ROUTE_NOT_INTERNET_GATEWAY');
    return { evaluation, subnet, vpcId };
  }

  const attachments = matching(
    world,
    command,
    GATEWAY_ATTACHMENT,
    (properties) =>
      properties['VpcId'] === vpcId &&
      properties['InternetGatewayId'] === gatewayId
  );
  if (attachments.length === 0) {
    addReason(evaluation, 'INTERNET_GATEWAY_NOT_ATTACHED');
    return { evaluation, subnet, vpcId };
  }
  if (attachments.length !== 1) {
    projection('Internet Gateway attachment is ambiguous');
  }
  const attachment = attachments[0];
  if (!attachment) projection('Internet Gateway attachment disappeared');
  const gateway = byReference(
    world,
    command,
    INTERNET_GATEWAY,
    gatewayId,
    'attached Internet Gateway'
  );
  evaluation.path.push(step(attachment, 'allow'), step(gateway, 'allow'));
  return { evaluation, subnet, vpcId };
}

function protocolMatches(value: unknown, protocol: TransportProtocol): boolean {
  if (value === '-1' || value === -1) return true;
  if (value === protocol) return true;
  if (protocol === 'tcp' && (value === '6' || value === 6)) return true;
  return protocol === 'udp' && (value === '17' || value === 17);
}

function portMatches(
  rule: Readonly<Record<string, unknown>>,
  protocol: TransportProtocol,
  port: number
): boolean {
  if (rule['IpProtocol'] === '-1' || rule['IpProtocol'] === -1) return true;
  if (!protocolMatches(rule['IpProtocol'], protocol)) return false;
  const from = rule['FromPort'];
  const to = rule['ToPort'];
  if (
    typeof from !== 'number' ||
    typeof to !== 'number' ||
    !Number.isSafeInteger(from) ||
    !Number.isSafeInteger(to) ||
    from < 0 ||
    to > 65_535 ||
    from > to
  ) {
    projection('security-group ingress port range is invalid');
  }
  return from <= port && port <= to;
}

function ingressRules(
  group: ResourceRecord
): readonly Readonly<Record<string, unknown>>[] {
  const value = stateObject(storedProperties(group))['ipPermissions'];
  return projectedArray(value, 'security-group ingress state').map(
    (entry, index) => projectedObject(entry, `security-group ingress[${index}]`)
  );
}

function externalRuleMatches(
  rule: Readonly<Record<string, unknown>>,
  requestValue: ReachabilityRequest
): boolean {
  if (!portMatches(rule, requestValue.protocol, requestValue.port))
    return false;
  const hasCidr = rule['CidrIp'] !== undefined;
  const hasSourceGroup = rule['SourceSecurityGroupId'] !== undefined;
  if (hasCidr && hasSourceGroup) {
    projection('security-group ingress source is ambiguous');
  }
  if (hasCidr) {
    return contains(
      cidr(rule['CidrIp'], 'security-group CidrIp', false),
      requestValue.source
    );
  }
  if (hasSourceGroup) return false;
  projection('security-group ingress source is not supported');
}

function targetRuleMatches(
  rule: Readonly<Record<string, unknown>>,
  protocol: TransportProtocol,
  port: number,
  sourceGroupIds: ReadonlySet<string>,
  sourceCidrs: readonly Ipv4Cidr[]
): boolean {
  if (!portMatches(rule, protocol, port)) return false;
  const sourceGroup = rule['SourceSecurityGroupId'];
  const hasCidr = rule['CidrIp'] !== undefined;
  if (sourceGroup !== undefined && hasCidr) {
    projection('target security-group ingress source is ambiguous');
  }
  if (typeof sourceGroup === 'string') return sourceGroupIds.has(sourceGroup);
  if (sourceGroup !== undefined) {
    projection('target security-group source group is invalid');
  }
  if (hasCidr) {
    const allowed = cidr(rule['CidrIp'], 'target security-group CidrIp', false);
    return sourceCidrs.some((source) => contains(allowed, source));
  }
  projection('target security-group ingress source is not supported');
}

function securityGroups(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  idsValue: unknown,
  vpcId: string,
  label: string
): readonly ResourceRecord[] {
  const ids = projectedStrings(idsValue, label);
  return ids.map((id) => {
    const group = byReference(
      world,
      command,
      SECURITY_GROUP,
      id,
      `${label} security group`
    );
    if (template(group)['VpcId'] !== vpcId) {
      projection(`${label} security group belongs to a different VPC`);
    }
    return group;
  });
}

function evaluateExternalIngress(
  groups: readonly ResourceRecord[],
  requestValue: ReachabilityRequest
): SecurityGroupEvaluation {
  let allowed = false;
  const path = groups.map((group) => {
    const groupAllows = ingressRules(group).some((rule) =>
      externalRuleMatches(rule, requestValue)
    );
    if (groupAllows) allowed = true;
    return step(group, groupAllows ? 'allow' : 'deny');
  });
  return { allowed, path };
}

function evaluateTargetIngress(
  groups: readonly ResourceRecord[],
  protocol: TransportProtocol,
  port: number,
  sourceGroups: readonly ResourceRecord[],
  sourceCidrs: readonly Ipv4Cidr[]
): SecurityGroupEvaluation {
  const sourceGroupIds = new Set(
    sourceGroups.map((group) => storedProperties(group).refValue)
  );
  let allowed = false;
  const path = groups.map((group) => {
    const groupAllows = ingressRules(group).some((rule) =>
      targetRuleMatches(rule, protocol, port, sourceGroupIds, sourceCidrs)
    );
    if (groupAllows) allowed = true;
    return step(group, groupAllows ? 'allow' : 'deny');
  });
  return { allowed, path };
}

function instancePublic(
  instance: ResourceRecord,
  subnet: ResourceRecord
): boolean {
  const instanceSetting = template(instance)['AssociatePublicIpAddress'];
  const subnetSetting = template(subnet)['MapPublicIpOnLaunch'];
  if (instanceSetting !== undefined && typeof instanceSetting !== 'boolean') {
    projection('instance AssociatePublicIpAddress is invalid');
  }
  if (subnetSetting !== undefined && typeof subnetSetting !== 'boolean') {
    projection('subnet MapPublicIpOnLaunch is invalid');
  }
  const configured =
    typeof instanceSetting === 'boolean' ? instanceSetting : subnetSetting;
  if (configured === undefined) {
    projection('public address assignment is not projected');
  }
  const publicIp = storedProperties(instance).attributes['PublicIp'];
  return configured && typeof publicIp === 'string' && publicIp.length > 0;
}

function running(instance: ResourceRecord): boolean {
  const state = stateObject(storedProperties(instance))['instanceState'];
  if (typeof state !== 'string' || !state) {
    projection('instance state is not projected');
  }
  return state === 'running';
}

function instanceResult(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  requestValue: ReachabilityRequest
): ProviderCommandResult {
  const instanceId = requestValue.destinationInstanceId;
  if (!instanceId) projection('destination instance ID disappeared');
  const instance = byReference(
    world,
    command,
    INSTANCE,
    instanceId,
    'destination instance'
  );
  const subnetId = projectedText(
    template(instance)['SubnetId'],
    'instance SubnetId'
  );
  const subnet = byReference(
    world,
    command,
    SUBNET,
    subnetId,
    'destination subnet'
  );
  const evaluation: Evaluation = { path: [], reasons: [] };
  if (!instancePublic(instance, subnet)) {
    addReason(evaluation, 'DESTINATION_NOT_PUBLIC');
  }
  const internet = internetPath(world, command, subnetId, evaluation);
  const groups = securityGroups(
    world,
    command,
    template(instance)['SecurityGroupIds'],
    internet.vpcId,
    'instance'
  );
  const ingress = evaluateExternalIngress(groups, requestValue);
  evaluation.path.push(...ingress.path);
  if (!ingress.allowed) {
    addReason(evaluation, 'SECURITY_GROUP_INGRESS_DENIED');
  }
  const isRunning = running(instance);
  if (!isRunning) addReason(evaluation, 'INSTANCE_NOT_RUNNING');
  evaluation.path.push(
    step(
      instance,
      isRunning && evaluation.reasons.length === 0 ? 'allow' : 'deny'
    )
  );
  return reachabilityResult(evaluation);
}

function listenerTransport(value: unknown): readonly TransportProtocol[] {
  const protocol = projectedText(value, 'listener Protocol').toUpperCase();
  switch (protocol) {
    case 'HTTP':
    case 'HTTPS':
    case 'TCP':
    case 'TLS':
      return ['tcp'];
    case 'UDP':
      return ['udp'];
    case 'TCP_UDP':
      return ['tcp', 'udp'];
    default:
      projection(`listener Protocol ${protocol} is not supported`);
  }
}

function targetTransport(value: unknown): TransportProtocol {
  const protocol = projectedText(value, 'target group Protocol').toUpperCase();
  switch (protocol) {
    case 'HTTP':
    case 'HTTPS':
    case 'TCP':
    case 'TLS':
      return 'tcp';
    case 'UDP':
      return 'udp';
    default:
      projection(`target group Protocol ${protocol} is not supported`);
  }
}

function projectedPort(value: unknown, label: string): number {
  if (
    typeof value !== 'number' ||
    !Number.isSafeInteger(value) ||
    value < 1 ||
    value > 65_535
  ) {
    projection(`${label} is invalid in the network projection`);
  }
  return value;
}

function webAclEvaluation(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  loadBalancer: ResourceRecord,
  loadBalancerArn: string,
  evaluation: Evaluation
): void {
  const association = stateObject(storedProperties(loadBalancer))['webAclArn'];
  if (association === undefined) {
    evaluation.path.push({
      resourceType: WEB_ACL_ASSOCIATION,
      resourceId: `none:${loadBalancer.resourceId}`,
      decision: 'not-associated',
    });
    return;
  }
  if (typeof association !== 'string' || !association) {
    projection('load balancer Web ACL association is invalid');
  }
  const webAcl = byReference(
    world,
    command,
    WEB_ACL,
    association,
    'associated Web ACL'
  );
  const associated = stateObject(storedProperties(webAcl))[
    'associatedResources'
  ];
  if (
    !Array.isArray(associated) ||
    associated.some((item) => typeof item !== 'string') ||
    !associated.includes(loadBalancerArn)
  ) {
    projection('Web ACL association state is inconsistent');
  }
  const rules = template(webAcl)['Rules'];
  if (rules !== undefined && (!Array.isArray(rules) || rules.length > 0)) {
    projection('WAF rules require HTTP request context');
  }
  const defaultAction = projectedObject(
    template(webAcl)['DefaultAction'],
    'Web ACL DefaultAction'
  );
  const allow = defaultAction['Allow'] !== undefined;
  const block = defaultAction['Block'] !== undefined;
  if (allow === block) projection('Web ACL DefaultAction is ambiguous');
  projectedObject(
    allow ? defaultAction['Allow'] : defaultAction['Block'],
    'Web ACL default action value'
  );
  if (block) addReason(evaluation, 'WAF_DEFAULT_BLOCK');
  evaluation.path.push({
    resourceType: WEB_ACL_ASSOCIATION,
    resourceId: webAcl.resourceId,
    decision: block ? 'deny' : 'allow',
  });
}

function listenerFor(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  loadBalancerArn: string,
  requestValue: ReachabilityRequest,
  evaluation: Evaluation
): ResourceRecord | undefined {
  const candidates = matching(world, command, LISTENER, (properties) => {
    if (
      properties['LoadBalancerArn'] !== loadBalancerArn ||
      properties['Port'] !== requestValue.port
    ) {
      return false;
    }
    return listenerTransport(properties['Protocol']).includes(
      requestValue.protocol
    );
  });
  if (candidates.length === 0) {
    addReason(evaluation, 'LOAD_BALANCER_LISTENER_NOT_FOUND');
    return undefined;
  }
  if (candidates.length !== 1)
    projection('load balancer listener is ambiguous');
  const listener = candidates[0];
  if (!listener) projection('load balancer listener disappeared');
  evaluation.path.push(step(listener, 'allow'));
  return listener;
}

function forwardTargetGroupArn(listener: ResourceRecord): string | undefined {
  const actions = projectedArray(
    template(listener)['DefaultActions'],
    'listener DefaultActions'
  );
  const forward = actions
    .map((action, index) =>
      projectedObject(action, `listener DefaultActions[${index}]`)
    )
    .filter((action) => String(action['Type']).toLowerCase() === 'forward');
  if (forward.length === 0) return undefined;
  if (forward.length !== 1) projection('listener forward action is ambiguous');
  const action = forward[0];
  if (!action) projection('listener forward action disappeared');
  const direct = action['TargetGroupArn'];
  const configuration = action['ForwardConfig'];
  let configured: unknown;
  if (configuration !== undefined) {
    const groups = projectedArray(
      projectedObject(configuration, 'listener ForwardConfig')['TargetGroups'],
      'listener ForwardConfig TargetGroups'
    );
    if (groups.length !== 1) projection('listener forward target is ambiguous');
    configured = projectedObject(groups[0], 'listener forward target')[
      'TargetGroupArn'
    ];
  }
  if (direct !== undefined && configured !== undefined) {
    projection('listener forward target is ambiguous');
  }
  return projectedText(direct ?? configured, 'listener forward TargetGroupArn');
}

function targetEntries(
  targetGroup: ResourceRecord
): readonly Readonly<Record<string, unknown>>[] {
  const entries = template(targetGroup)['Targets'];
  if (entries === undefined) return [];
  return projectedArray(entries, 'target group Targets').map((entry, index) =>
    projectedObject(entry, `target group Targets[${index}]`)
  );
}

function evaluateTarget(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  entry: Readonly<Record<string, unknown>>,
  targetGroup: ResourceRecord,
  vpcId: string,
  loadBalancerGroups: readonly ResourceRecord[],
  sourceCidrs: readonly Ipv4Cidr[]
): TargetEvaluation {
  const instanceId = projectedText(entry['Id'], 'target instance ID');
  const instance = byReference(
    world,
    command,
    INSTANCE,
    instanceId,
    'target instance'
  );
  const subnetId = projectedText(
    template(instance)['SubnetId'],
    'target instance SubnetId'
  );
  const subnet = byReference(
    world,
    command,
    SUBNET,
    subnetId,
    'target instance subnet'
  );
  if (template(subnet)['VpcId'] !== vpcId) {
    projection('target instance belongs to a different VPC');
  }
  const protocol = targetTransport(template(targetGroup)['Protocol']);
  const port = projectedPort(
    entry['Port'] ?? template(targetGroup)['Port'],
    'target group port'
  );
  const groups = securityGroups(
    world,
    command,
    template(instance)['SecurityGroupIds'],
    vpcId,
    'target instance'
  );
  const ingress = evaluateTargetIngress(
    groups,
    protocol,
    port,
    loadBalancerGroups,
    sourceCidrs
  );
  const reasons: string[] = [];
  if (!ingress.allowed) reasons.push('TARGET_SECURITY_GROUP_INGRESS_DENIED');
  const isRunning = running(instance);
  if (!isRunning) reasons.push('TARGET_INSTANCE_NOT_RUNNING');
  return {
    allowed: reasons.length === 0,
    instance,
    path: [...ingress.path, step(instance, reasons.length ? 'deny' : 'allow')],
    reasons,
  };
}

function targetGroupEvaluation(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  listener: ResourceRecord,
  vpcId: string,
  loadBalancerGroups: readonly ResourceRecord[],
  sourceCidrs: readonly Ipv4Cidr[],
  evaluation: Evaluation
): void {
  const arn = forwardTargetGroupArn(listener);
  if (!arn) {
    addReason(evaluation, 'LISTENER_NOT_FORWARDING');
    return;
  }
  const targetGroup = byReference(
    world,
    command,
    TARGET_GROUP,
    arn,
    'listener target group'
  );
  const targetType = template(targetGroup)['TargetType'] ?? 'instance';
  if (targetType !== 'instance') {
    projection('listener target group is not instance-backed');
  }
  if (template(targetGroup)['VpcId'] !== vpcId) {
    projection('listener target group belongs to a different VPC');
  }
  evaluation.path.push(step(targetGroup, 'allow'));
  const entries = targetEntries(targetGroup);
  if (entries.length === 0) {
    addReason(evaluation, 'TARGET_GROUP_EMPTY');
    return;
  }
  const ids = entries.map((entry) =>
    projectedText(entry['Id'], 'target instance ID')
  );
  if (new Set(ids).size !== ids.length) {
    projection('target group contains duplicate targets');
  }
  const candidates = entries
    .map((entry) =>
      evaluateTarget(
        world,
        command,
        entry,
        targetGroup,
        vpcId,
        loadBalancerGroups,
        sourceCidrs
      )
    )
    .sort((left, right) =>
      left.instance.resourceId.localeCompare(right.instance.resourceId)
    );
  const selected =
    candidates.find((candidate) => candidate.allowed) ?? candidates[0];
  if (!selected) projection('target group evaluation disappeared');
  if (selected.allowed) {
    evaluation.path.push(...selected.path);
    return;
  }
  for (const candidate of candidates) {
    evaluation.path.push(...candidate.path);
    for (const reason of candidate.reasons) addReason(evaluation, reason);
  }
}

function loadBalancerResult(
  world: ProviderWorldView,
  command: ProviderCommandInput,
  requestValue: ReachabilityRequest
): ProviderCommandResult {
  const arn = requestValue.destinationLoadBalancerArn;
  if (!arn) projection('destination load balancer ARN disappeared');
  const loadBalancer = byReference(
    world,
    command,
    LOAD_BALANCER,
    arn,
    'destination load balancer'
  );
  const evaluation: Evaluation = { path: [], reasons: [] };
  const loadBalancerType = template(loadBalancer)['Type'] ?? 'application';
  if (loadBalancerType !== 'application') {
    projection('destination load balancer is not an ALB');
  }
  const scheme = template(loadBalancer)['Scheme'] ?? 'internet-facing';
  if (scheme !== 'internet-facing' && scheme !== 'internal') {
    projection('load balancer Scheme is invalid');
  }
  if (scheme === 'internal') addReason(evaluation, 'DESTINATION_NOT_PUBLIC');
  const subnetIds = projectedStrings(
    template(loadBalancer)['Subnets'],
    'load balancer Subnets'
  );
  const internetPaths = subnetIds.map((subnetId) =>
    internetPath(world, command, subnetId, evaluation)
  );
  const vpcIds = new Set(internetPaths.map((path) => path.vpcId));
  if (vpcIds.size !== 1) {
    projection('load balancer subnets belong to different VPCs');
  }
  const vpcId = internetPaths[0]?.vpcId;
  if (!vpcId) projection('load balancer VPC disappeared');
  const sourceCidrs = internetPaths.map((path, index) =>
    cidr(
      template(path.subnet)['CidrBlock'],
      `load balancer Subnets[${index}] CidrBlock`,
      false
    )
  );
  const groups = securityGroups(
    world,
    command,
    template(loadBalancer)['SecurityGroups'],
    vpcId,
    'load balancer'
  );
  const ingress = evaluateExternalIngress(groups, requestValue);
  evaluation.path.push(...ingress.path);
  if (!ingress.allowed) {
    addReason(evaluation, 'SECURITY_GROUP_INGRESS_DENIED');
  }
  evaluation.path.push(
    step(loadBalancer, evaluation.reasons.length ? 'deny' : 'allow')
  );
  webAclEvaluation(world, command, loadBalancer, arn, evaluation);
  const listener = listenerFor(world, command, arn, requestValue, evaluation);
  if (listener) {
    targetGroupEvaluation(
      world,
      command,
      listener,
      vpcId,
      groups,
      sourceCidrs,
      evaluation
    );
  }
  return reachabilityResult(evaluation);
}

function reachabilityResult(evaluation: Evaluation): ProviderCommandResult {
  return result('AwsEc2ReachabilityEvaluated', {
    reachable: evaluation.reasons.length === 0,
    reasons: evaluation.reasons,
    path: evaluation.path,
  });
}

export function evaluateReachability(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const requestValue = request(command);
  return requestValue.destinationInstanceId
    ? instanceResult(world, command, requestValue)
    : loadBalancerResult(world, command, requestValue);
}
