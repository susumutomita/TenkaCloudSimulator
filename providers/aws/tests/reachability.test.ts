import { afterEach, describe, expect, it } from 'bun:test';
import type {
  ProviderCommandInput,
  ResourceRecord,
} from '@tenkacloud/simulator-core';
import { AWS_CATALOG_CAPABILITY_MANIFEST } from '../src/catalog-manifest';
import { AWS_CAPABILITIES } from '../src/model';
import { AwsProvider } from '../src/provider';
import {
  cleanupContexts,
  createContext,
  execute,
  resourceByLogicalId,
  type TestContext,
} from './support';

const INSTANCE_RESOURCE = 'AWS::EC2::Instance';

afterEach(cleanupContexts);

function networkContext(): Promise<TestContext> {
  return createContext(undefined, 'network-reachability', 'network-stack.yaml');
}

function reference(context: TestContext, logicalId: string): string {
  return String(resourceByLogicalId(context, logicalId).properties['refValue']);
}

function attributes(
  context: TestContext,
  logicalId: string
): Readonly<Record<string, unknown>> {
  const value = resourceByLogicalId(context, logicalId).properties[
    'attributes'
  ];
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${logicalId} attributes are missing`);
  }
  return Object.fromEntries(Object.entries(value));
}

function evaluate(
  context: TestContext,
  input: Readonly<Record<string, unknown>>,
  resourceType = INSTANCE_RESOURCE
): Readonly<Record<string, unknown>> {
  return execute(context, 'ec2', 'EvaluateReachability', input, resourceType);
}

function evaluateDirect(
  context: TestContext,
  input: Readonly<Record<string, unknown>>,
  resourceType: string
): Readonly<Record<string, unknown>> {
  const command: ProviderCommandInput = {
    worldId: context.worldId,
    deploymentId: context.deploymentId,
    service: 'ec2',
    operation: 'EvaluateReachability',
    resourceType,
    input,
  };
  return new AwsProvider().reduce(command, {
    world: context.core.world(context.worldId),
    resources: context.store.resources(context.worldId),
  }).response;
}

function saveResource(context: TestContext, resource: ResourceRecord): void {
  context.store.transaction(() => context.store.saveResource(resource));
}

function updateProperties(
  context: TestContext,
  logicalId: string,
  transform: (
    properties: Readonly<Record<string, unknown>>
  ) => Readonly<Record<string, unknown>>
): void {
  const resource = resourceByLogicalId(context, logicalId);
  saveResource(context, {
    ...resource,
    properties: transform(resource.properties),
  });
}

function updateTemplate(
  context: TestContext,
  logicalId: string,
  transform: (
    template: Readonly<Record<string, unknown>>
  ) => Readonly<Record<string, unknown>>
): void {
  updateProperties(context, logicalId, (properties) => {
    const value = properties['templateProperties'];
    if (value === null || typeof value !== 'object' || Array.isArray(value)) {
      throw new Error(`${logicalId} template is missing`);
    }
    return {
      ...properties,
      templateProperties: transform(Object.fromEntries(Object.entries(value))),
    };
  });
}

function updateState(
  context: TestContext,
  logicalId: string,
  state: Readonly<Record<string, unknown>>
): void {
  updateProperties(context, logicalId, (properties) => ({
    ...properties,
    state,
  }));
}

function deleteProjection(context: TestContext, logicalId: string): void {
  const resource = resourceByLogicalId(context, logicalId);
  saveResource(context, { ...resource, status: 'deleted' });
}

function directInput(context: TestContext): Readonly<Record<string, unknown>> {
  return {
    SourceCidr: '203.0.113.0/24',
    IpProtocol: 'tcp',
    Port: 80,
    DestinationInstanceId: reference(context, 'DirectInstance'),
  };
}

function loadBalancerInput(
  context: TestContext,
  overrides: Readonly<Record<string, unknown>> = {}
): Readonly<Record<string, unknown>> {
  return {
    SourceCidr: '198.51.100.0/24',
    IpProtocol: 'tcp',
    Port: 443,
    DestinationLoadBalancerArn: reference(context, 'LoadBalancer'),
    ...overrides,
  };
}

function addOtherVpcSubnet(context: TestContext): string {
  const vpc = resourceByLogicalId(context, 'Vpc');
  const subnet = resourceByLogicalId(context, 'PrivateSubnet');
  const otherVpcId = 'vpc-other-projection';
  const otherSubnetId = 'subnet-other-projection';
  const subnetTemplate = subnet.properties['templateProperties'];
  if (
    subnetTemplate === null ||
    typeof subnetTemplate !== 'object' ||
    Array.isArray(subnetTemplate)
  ) {
    throw new Error('private subnet template is missing');
  }
  saveResource(context, {
    ...vpc,
    resourceId: `${vpc.resourceId}-other`,
    properties: {
      ...vpc.properties,
      logicalId: 'OtherVpc',
      physicalId: otherVpcId,
      refValue: otherVpcId,
    },
  });
  saveResource(context, {
    ...subnet,
    resourceId: `${subnet.resourceId}-other`,
    properties: {
      ...subnet.properties,
      logicalId: 'OtherSubnet',
      physicalId: otherSubnetId,
      refValue: otherSubnetId,
      templateProperties: {
        ...Object.fromEntries(Object.entries(subnetTemplate)),
        VpcId: otherVpcId,
      },
    },
  });
  return otherSubnetId;
}

function pathTypes(response: Readonly<Record<string, unknown>>): string[] {
  const path = response['path'];
  if (!Array.isArray(path)) throw new Error('path is missing');
  return path.map((entry) => {
    if (entry === null || typeof entry !== 'object') {
      throw new Error('path entry is invalid');
    }
    return String(Reflect.get(entry, 'resourceType'));
  });
}

describe('AWS L3 reachability projection', () => {
  it('public subnet の instance と ALB target までの現在 resource path を決定的に返す', async () => {
    const context = await networkContext();
    const direct = evaluate(context, {
      SourceCidr: '203.0.113.64/26',
      IpProtocol: 'tcp',
      Port: 80,
      DestinationInstanceId: reference(context, 'DirectInstance'),
    });
    expect(direct).toMatchObject({ reachable: true, reasons: [] });
    expect(
      evaluate(context, {
        SourceCidr: '203.0.113.64/26',
        IpProtocol: 'tcp',
        Port: 80,
        DestinationInstanceId: reference(context, 'DirectInstance'),
      })
    ).toEqual(direct);
    expect(pathTypes(direct)).toEqual([
      'AWS::EC2::VPC',
      'AWS::EC2::Subnet',
      'AWS::EC2::SubnetRouteTableAssociation',
      'AWS::EC2::RouteTable',
      'AWS::EC2::Route',
      'AWS::EC2::VPCGatewayAttachment',
      'AWS::EC2::InternetGateway',
      'AWS::EC2::SecurityGroup',
      'AWS::EC2::Instance',
    ]);

    const loadBalancer = evaluate(context, {
      SourceCidr: '198.51.100.128/25',
      IpProtocol: 'tcp',
      Port: 443,
      DestinationLoadBalancerArn: reference(context, 'LoadBalancer'),
    });
    expect(loadBalancer).toMatchObject({ reachable: true, reasons: [] });
    expect(pathTypes(loadBalancer)).toEqual([
      'AWS::EC2::VPC',
      'AWS::EC2::Subnet',
      'AWS::EC2::SubnetRouteTableAssociation',
      'AWS::EC2::RouteTable',
      'AWS::EC2::Route',
      'AWS::EC2::VPCGatewayAttachment',
      'AWS::EC2::InternetGateway',
      'AWS::EC2::SecurityGroup',
      'AWS::ElasticLoadBalancingV2::LoadBalancer',
      'AWS::WAFv2::WebACLAssociation',
      'AWS::ElasticLoadBalancingV2::Listener',
      'AWS::ElasticLoadBalancingV2::TargetGroup',
      'AWS::EC2::SecurityGroup',
      'AWS::EC2::Instance',
    ]);
    expect(
      AWS_CAPABILITIES.find(
        (capability) => capability.operation === 'EvaluateReachability'
      )
    ).toMatchObject({
      service: 'ec2',
      resourceType: INSTANCE_RESOURCE,
      fidelity: ['L0', 'L1', 'L3'],
    });
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) => capability.operation === 'EvaluateReachability'
      )
    ).toMatchObject({
      service: 'ec2',
      resourceType: INSTANCE_RESOURCE,
      fidelity: 'L3',
    });
  });

  it('private subnet、public address、route、現在 SG ingress の deny 理由を隠さない', async () => {
    const context = await networkContext();
    const privateResult = evaluate(context, {
      SourceCidr: '203.0.113.0/24',
      IpProtocol: 'tcp',
      Port: 80,
      DestinationInstanceId: reference(context, 'PrivateInstance'),
    });
    expect(privateResult).toMatchObject({
      reachable: false,
      reasons: ['DESTINATION_NOT_PUBLIC', 'SUBNET_ROUTE_TABLE_UNASSOCIATED'],
    });

    const directGroup = reference(context, 'DirectSecurityGroup');
    execute(context, 'ec2', 'RevokeSecurityGroupIngress', {
      GroupId: directGroup,
      IpPermissions: [
        {
          IpProtocol: 'tcp',
          FromPort: 80,
          ToPort: 80,
          CidrIp: '203.0.113.0/24',
        },
      ],
    });
    expect(
      evaluate(context, {
        SourceCidr: '203.0.113.0/24',
        IpProtocol: 'tcp',
        Port: 80,
        DestinationInstanceId: reference(context, 'DirectInstance'),
      })
    ).toMatchObject({
      reachable: false,
      reasons: ['SECURITY_GROUP_INGRESS_DENIED'],
    });
  });

  it('ALB listener、target ingress、instance state、Web ACL default action を実 state から評価する', async () => {
    const context = await networkContext();
    const loadBalancerArn = reference(context, 'LoadBalancer');
    const baseInput = {
      SourceCidr: '198.51.100.0/24',
      IpProtocol: 'tcp',
      Port: 443,
      DestinationLoadBalancerArn: loadBalancerArn,
    };
    const allowArn = String(attributes(context, 'AllowWebAcl')['Arn']);
    execute(context, 'wafv2', 'AssociateWebACL', {
      WebACLArn: allowArn,
      ResourceArn: loadBalancerArn,
    });
    expect(evaluate(context, baseInput)).toMatchObject({ reachable: true });

    execute(context, 'wafv2', 'DisassociateWebACL', {
      ResourceArn: loadBalancerArn,
    });
    const blockArn = String(attributes(context, 'BlockWebAcl')['Arn']);
    execute(context, 'wafv2', 'AssociateWebACL', {
      WebACLArn: blockArn,
      ResourceArn: loadBalancerArn,
    });
    expect(evaluate(context, baseInput)).toMatchObject({
      reachable: false,
      reasons: ['WAF_DEFAULT_BLOCK'],
    });

    execute(context, 'wafv2', 'DisassociateWebACL', {
      ResourceArn: loadBalancerArn,
    });
    updateProperties(context, 'TargetInstance', (properties) => ({
      ...properties,
      state: { instanceState: 'stopped' },
    }));
    expect(evaluate(context, baseInput)).toMatchObject({
      reachable: false,
      reasons: ['TARGET_INSTANCE_NOT_RUNNING'],
    });
  });

  it('listener または target security group が request と一致しない既知状態を deny にする', async () => {
    const context = await networkContext();
    const loadBalancerArn = reference(context, 'LoadBalancer');
    expect(
      evaluate(context, {
        SourceCidr: '198.51.100.0/24',
        IpProtocol: 'tcp',
        Port: 444,
        DestinationLoadBalancerArn: loadBalancerArn,
      })
    ).toMatchObject({
      reachable: false,
      reasons: [
        'SECURITY_GROUP_INGRESS_DENIED',
        'LOAD_BALANCER_LISTENER_NOT_FOUND',
      ],
    });

    execute(context, 'ec2', 'RevokeSecurityGroupIngress', {
      GroupId: reference(context, 'TargetSecurityGroup'),
      IpProtocol: 'tcp',
      FromPort: 8080,
      ToPort: 8080,
      SourceSecurityGroupId: reference(context, 'LoadBalancerSecurityGroup'),
    });
    expect(
      evaluate(context, {
        SourceCidr: '198.51.100.0/24',
        IpProtocol: 'tcp',
        Port: 443,
        DestinationLoadBalancerArn: loadBalancerArn,
      })
    ).toMatchObject({
      reachable: false,
      reasons: ['TARGET_SECURITY_GROUP_INGRESS_DENIED'],
    });
  });

  it('default route と Internet Gateway attachment の欠落を既知の deny として区別する', async () => {
    const missingRoute = await networkContext();
    deleteProjection(missingRoute, 'PublicDefaultRoute');
    expect(evaluate(missingRoute, directInput(missingRoute))).toMatchObject({
      reachable: false,
      reasons: ['DEFAULT_ROUTE_MISSING'],
    });

    const nonGatewayRoute = await networkContext();
    updateTemplate(nonGatewayRoute, 'PublicDefaultRoute', (template) => {
      const { GatewayId: _gateway, ...rest } = template;
      return { ...rest, NatGatewayId: 'nat-unprojected' };
    });
    expect(
      evaluate(nonGatewayRoute, directInput(nonGatewayRoute))
    ).toMatchObject({
      reachable: false,
      reasons: ['DEFAULT_ROUTE_NOT_INTERNET_GATEWAY'],
    });

    const missingAttachment = await networkContext();
    deleteProjection(missingAttachment, 'GatewayAttachment');
    expect(
      evaluate(missingAttachment, directInput(missingAttachment))
    ).toMatchObject({
      reachable: false,
      reasons: ['INTERNET_GATEWAY_NOT_ATTACHED'],
    });
  });

  it('TCP_UDP と UDP listener、numeric protocol、target CIDR ingress を同じ projection で評価する', async () => {
    const numeric = await networkContext();
    updateState(numeric, 'DirectSecurityGroup', {
      ipPermissions: [
        {
          IpProtocol: 6,
          FromPort: 80,
          ToPort: 80,
          CidrIp: '203.0.113.0/24',
        },
      ],
      ipPermissionsEgress: [],
    });
    expect(evaluate(numeric, directInput(numeric))).toMatchObject({
      reachable: true,
    });

    const combined = await networkContext();
    updateTemplate(combined, 'Listener', (template) => ({
      ...template,
      Protocol: 'TCP_UDP',
    }));
    expect(evaluate(combined, loadBalancerInput(combined))).toMatchObject({
      reachable: true,
    });

    const udp = await networkContext();
    updateState(udp, 'LoadBalancerSecurityGroup', {
      ipPermissions: [
        {
          IpProtocol: 'udp',
          FromPort: 443,
          ToPort: 443,
          CidrIp: '198.51.100.0/24',
        },
      ],
      ipPermissionsEgress: [],
    });
    updateTemplate(udp, 'Listener', (template) => ({
      ...template,
      Protocol: 'UDP',
    }));
    updateTemplate(udp, 'TargetGroup', (template) => ({
      ...template,
      Protocol: 'UDP',
    }));
    updateState(udp, 'TargetSecurityGroup', {
      ipPermissions: [
        {
          IpProtocol: 17,
          FromPort: 8080,
          ToPort: 8080,
          CidrIp: '10.20.1.0/24',
        },
      ],
      ipPermissionsEgress: [],
    });
    expect(
      evaluate(udp, loadBalancerInput(udp, { IpProtocol: 'udp' }))
    ).toMatchObject({ reachable: true, reasons: [] });
  });

  it('壊れた route、public address、security group projection を allow に丸めない', async () => {
    const routeVpc = await networkContext();
    updateTemplate(routeVpc, 'PublicRouteTable', (template) => ({
      ...template,
      VpcId: 'vpc-other',
    }));
    expect(() => evaluate(routeVpc, directInput(routeVpc))).toThrow(
      'different VPC'
    );

    const duplicateAttachment = await networkContext();
    const attachment = resourceByLogicalId(
      duplicateAttachment,
      'GatewayAttachment'
    );
    saveResource(duplicateAttachment, {
      ...attachment,
      resourceId: `${attachment.resourceId}-duplicate`,
      properties: {
        ...attachment.properties,
        logicalId: 'DuplicateGatewayAttachment',
      },
    });
    expect(() =>
      evaluate(duplicateAttachment, directInput(duplicateAttachment))
    ).toThrow('Internet Gateway attachment is ambiguous');

    const duplicateGroups = await networkContext();
    const groupId = reference(duplicateGroups, 'DirectSecurityGroup');
    updateTemplate(duplicateGroups, 'DirectInstance', (template) => ({
      ...template,
      SecurityGroupIds: [groupId, groupId],
    }));
    expect(() =>
      evaluate(duplicateGroups, directInput(duplicateGroups))
    ).toThrow('duplicate references');

    const groupVpc = await networkContext();
    updateTemplate(groupVpc, 'DirectSecurityGroup', (template) => ({
      ...template,
      VpcId: 'vpc-other',
    }));
    expect(() => evaluate(groupVpc, directInput(groupVpc))).toThrow(
      'security group belongs to a different VPC'
    );

    const invalidInstanceSetting = await networkContext();
    updateTemplate(invalidInstanceSetting, 'DirectInstance', (template) => ({
      ...template,
      AssociatePublicIpAddress: 'yes',
    }));
    expect(() =>
      evaluate(invalidInstanceSetting, directInput(invalidInstanceSetting))
    ).toThrow('AssociatePublicIpAddress is invalid');

    const invalidSubnetSetting = await networkContext();
    updateTemplate(invalidSubnetSetting, 'DirectInstance', (template) => {
      const { AssociatePublicIpAddress: _setting, ...rest } = template;
      return rest;
    });
    updateTemplate(invalidSubnetSetting, 'PublicSubnet', (template) => ({
      ...template,
      MapPublicIpOnLaunch: 'yes',
    }));
    expect(() =>
      evaluate(invalidSubnetSetting, directInput(invalidSubnetSetting))
    ).toThrow('MapPublicIpOnLaunch is invalid');

    const missingPublicProjection = await networkContext();
    updateTemplate(missingPublicProjection, 'DirectInstance', (template) => {
      const { AssociatePublicIpAddress: _setting, ...rest } = template;
      return rest;
    });
    updateTemplate(missingPublicProjection, 'PublicSubnet', (template) => {
      const { MapPublicIpOnLaunch: _setting, ...rest } = template;
      return rest;
    });
    expect(() =>
      evaluate(missingPublicProjection, directInput(missingPublicProjection))
    ).toThrow('public address assignment is not projected');

    const missingState = await networkContext();
    updateState(missingState, 'DirectInstance', {});
    expect(() => evaluate(missingState, directInput(missingState))).toThrow(
      'instance state is not projected'
    );

    const missingSubnet = await networkContext();
    updateTemplate(missingSubnet, 'DirectInstance', (template) => {
      const { SubnetId: _subnet, ...rest } = template;
      return rest;
    });
    expect(() => evaluate(missingSubnet, directInput(missingSubnet))).toThrow(
      'instance SubnetId is missing'
    );
  });

  it('不正な ingress state、CIDR、port range、source を projection conflict にする', async () => {
    const invalidState = await networkContext();
    updateState(invalidState, 'DirectSecurityGroup', {
      ipPermissions: 'invalid',
    });
    expect(() => evaluate(invalidState, directInput(invalidState))).toThrow(
      'ingress state is invalid'
    );

    const invalidObject = await networkContext();
    updateState(invalidObject, 'DirectSecurityGroup', {
      ipPermissions: [null],
    });
    expect(() => evaluate(invalidObject, directInput(invalidObject))).toThrow(
      'ingress[0] is invalid'
    );

    const cases = [
      {
        rule: {
          IpProtocol: 'tcp',
          FromPort: 80,
          ToPort: 80,
          CidrIp: 123,
        },
        message: 'CidrIp is invalid',
      },
      {
        rule: {
          IpProtocol: 'tcp',
          FromPort: 80,
          ToPort: 80,
          CidrIp: 'invalid',
        },
        message: 'CidrIp is invalid',
      },
      {
        rule: {
          IpProtocol: 'tcp',
          FromPort: 80,
          ToPort: 80,
          CidrIp: '300.0.0.0/24',
        },
        message: 'CidrIp is invalid',
      },
      {
        rule: {
          IpProtocol: 'tcp',
          FromPort: 80,
          ToPort: 80,
          CidrIp: '203.0.113.1/24',
        },
        message: 'CidrIp is not canonical',
      },
      {
        rule: {
          IpProtocol: 'tcp',
          FromPort: 81,
          ToPort: 80,
          CidrIp: '203.0.113.0/24',
        },
        message: 'port range is invalid',
      },
      {
        rule: { IpProtocol: 'tcp', FromPort: 80, ToPort: 80 },
        message: 'ingress source is not supported',
      },
    ];
    for (const entry of cases) {
      const context = await networkContext();
      updateState(context, 'DirectSecurityGroup', {
        ipPermissions: [entry.rule],
      });
      expect(() => evaluate(context, directInput(context))).toThrow(
        entry.message
      );
    }

    const sourceGroupOnly = await networkContext();
    updateState(sourceGroupOnly, 'DirectSecurityGroup', {
      ipPermissions: [
        {
          IpProtocol: 'tcp',
          FromPort: 80,
          ToPort: 80,
          SourceSecurityGroupId: reference(
            sourceGroupOnly,
            'LoadBalancerSecurityGroup'
          ),
        },
      ],
    });
    expect(
      evaluate(sourceGroupOnly, directInput(sourceGroupOnly))
    ).toMatchObject({
      reachable: false,
      reasons: ['SECURITY_GROUP_INGRESS_DENIED'],
    });

    const ambiguousSource = await networkContext();
    updateState(ambiguousSource, 'DirectSecurityGroup', {
      ipPermissions: [
        {
          IpProtocol: 'tcp',
          FromPort: 80,
          ToPort: 80,
          CidrIp: '203.0.113.0/24',
          SourceSecurityGroupId: reference(
            ambiguousSource,
            'LoadBalancerSecurityGroup'
          ),
        },
      ],
    });
    expect(() =>
      evaluate(ambiguousSource, directInput(ambiguousSource))
    ).toThrow('ingress source is ambiguous');

    const unsupportedTargetSource = await networkContext();
    updateState(unsupportedTargetSource, 'TargetSecurityGroup', {
      ipPermissions: [
        {
          IpProtocol: 'tcp',
          FromPort: 8080,
          ToPort: 8080,
          SourcePrefixListId: 'pl-unsupported',
        },
      ],
    });
    expect(() =>
      evaluate(
        unsupportedTargetSource,
        loadBalancerInput(unsupportedTargetSource)
      )
    ).toThrow('target security-group ingress source is not supported');

    const ambiguousTargetSource = await networkContext();
    updateState(ambiguousTargetSource, 'TargetSecurityGroup', {
      ipPermissions: [
        {
          IpProtocol: 'tcp',
          FromPort: 8080,
          ToPort: 8080,
          CidrIp: '10.20.1.0/24',
          SourceSecurityGroupId: reference(
            ambiguousTargetSource,
            'LoadBalancerSecurityGroup'
          ),
        },
      ],
    });
    expect(() =>
      evaluate(ambiguousTargetSource, loadBalancerInput(ambiguousTargetSource))
    ).toThrow('target security-group ingress source is ambiguous');

    const invalidTargetSourceGroup = await networkContext();
    updateState(invalidTargetSourceGroup, 'TargetSecurityGroup', {
      ipPermissions: [
        {
          IpProtocol: 'tcp',
          FromPort: 8080,
          ToPort: 8080,
          SourceSecurityGroupId: 123,
        },
      ],
    });
    expect(() =>
      evaluate(
        invalidTargetSourceGroup,
        loadBalancerInput(invalidTargetSourceGroup)
      )
    ).toThrow('target security-group source group is invalid');
  });

  it('ALB の protocol、forward action、target group、WAF projection の未知状態を loud に拒否する', async () => {
    const listenerProtocol = await networkContext();
    updateTemplate(listenerProtocol, 'Listener', (template) => ({
      ...template,
      Protocol: 'SCTP',
    }));
    expect(() =>
      evaluate(listenerProtocol, loadBalancerInput(listenerProtocol))
    ).toThrow('listener Protocol SCTP is not supported');

    const targetProtocol = await networkContext();
    updateTemplate(targetProtocol, 'TargetGroup', (template) => ({
      ...template,
      Protocol: 'SCTP',
    }));
    expect(() =>
      evaluate(targetProtocol, loadBalancerInput(targetProtocol))
    ).toThrow('target group Protocol SCTP is not supported');

    const targetPort = await networkContext();
    updateTemplate(targetPort, 'TargetGroup', (template) => ({
      ...template,
      Port: 0,
    }));
    expect(() => evaluate(targetPort, loadBalancerInput(targetPort))).toThrow(
      'target group port is invalid'
    );

    const invalidAssociation = await networkContext();
    updateState(invalidAssociation, 'LoadBalancer', { webAclArn: 123 });
    expect(() =>
      evaluate(invalidAssociation, loadBalancerInput(invalidAssociation))
    ).toThrow('Web ACL association is invalid');

    const inconsistentAssociation = await networkContext();
    const inconsistentLoadBalancer = reference(
      inconsistentAssociation,
      'LoadBalancer'
    );
    execute(inconsistentAssociation, 'wafv2', 'AssociateWebACL', {
      WebACLArn: String(
        attributes(inconsistentAssociation, 'AllowWebAcl')['Arn']
      ),
      ResourceArn: inconsistentLoadBalancer,
    });
    updateState(inconsistentAssociation, 'AllowWebAcl', {
      associatedResources: [],
    });
    expect(() =>
      evaluate(
        inconsistentAssociation,
        loadBalancerInput(inconsistentAssociation)
      )
    ).toThrow('association state is inconsistent');

    const invalidDefaultAction = await networkContext();
    const invalidActionLoadBalancer = reference(
      invalidDefaultAction,
      'LoadBalancer'
    );
    updateTemplate(invalidDefaultAction, 'AllowWebAcl', (template) => ({
      ...template,
      DefaultAction: { Allow: 'invalid' },
    }));
    execute(invalidDefaultAction, 'wafv2', 'AssociateWebACL', {
      WebACLArn: String(attributes(invalidDefaultAction, 'AllowWebAcl')['Arn']),
      ResourceArn: invalidActionLoadBalancer,
    });
    expect(() =>
      evaluate(invalidDefaultAction, loadBalancerInput(invalidDefaultAction))
    ).toThrow('default action value is invalid');

    const forwardConfig = await networkContext();
    const targetGroupArn = reference(forwardConfig, 'TargetGroup');
    updateTemplate(forwardConfig, 'Listener', (template) => ({
      ...template,
      DefaultActions: [
        {
          Type: 'forward',
          ForwardConfig: {
            TargetGroups: [{ TargetGroupArn: targetGroupArn }],
          },
        },
      ],
    }));
    expect(
      evaluate(forwardConfig, loadBalancerInput(forwardConfig))
    ).toMatchObject({ reachable: true });

    const ambiguousForward = await networkContext();
    const ambiguousTargetGroup = reference(ambiguousForward, 'TargetGroup');
    updateTemplate(ambiguousForward, 'Listener', (template) => ({
      ...template,
      DefaultActions: [
        {
          Type: 'forward',
          TargetGroupArn: ambiguousTargetGroup,
          ForwardConfig: {
            TargetGroups: [{ TargetGroupArn: ambiguousTargetGroup }],
          },
        },
      ],
    }));
    expect(() =>
      evaluate(ambiguousForward, loadBalancerInput(ambiguousForward))
    ).toThrow('listener forward target is ambiguous');

    const fixedResponse = await networkContext();
    updateTemplate(fixedResponse, 'Listener', (template) => ({
      ...template,
      DefaultActions: [{ Type: 'fixed-response' }],
    }));
    expect(
      evaluate(fixedResponse, loadBalancerInput(fixedResponse))
    ).toMatchObject({
      reachable: false,
      reasons: ['LISTENER_NOT_FORWARDING'],
    });

    const targetType = await networkContext();
    updateTemplate(targetType, 'TargetGroup', (template) => ({
      ...template,
      TargetType: 'lambda',
    }));
    expect(() => evaluate(targetType, loadBalancerInput(targetType))).toThrow(
      'not instance-backed'
    );

    const targetVpc = await networkContext();
    updateTemplate(targetVpc, 'TargetGroup', (template) => ({
      ...template,
      VpcId: 'vpc-other',
    }));
    expect(() => evaluate(targetVpc, loadBalancerInput(targetVpc))).toThrow(
      'target group belongs to a different VPC'
    );

    const emptyTargets = await networkContext();
    updateTemplate(emptyTargets, 'TargetGroup', (template) => ({
      ...template,
      Targets: [],
    }));
    expect(
      evaluate(emptyTargets, loadBalancerInput(emptyTargets))
    ).toMatchObject({ reachable: false, reasons: ['TARGET_GROUP_EMPTY'] });

    const duplicateTargets = await networkContext();
    const targetId = reference(duplicateTargets, 'TargetInstance');
    updateTemplate(duplicateTargets, 'TargetGroup', (template) => ({
      ...template,
      Targets: [{ Id: targetId }, { Id: targetId }],
    }));
    expect(() =>
      evaluate(duplicateTargets, loadBalancerInput(duplicateTargets))
    ).toThrow('duplicate targets');

    const invalidScheme = await networkContext();
    updateTemplate(invalidScheme, 'LoadBalancer', (template) => ({
      ...template,
      Scheme: 'unknown',
    }));
    expect(() =>
      evaluate(invalidScheme, loadBalancerInput(invalidScheme))
    ).toThrow('Scheme is invalid');

    const networkLoadBalancer = await networkContext();
    updateTemplate(networkLoadBalancer, 'LoadBalancer', (template) => ({
      ...template,
      Type: 'network',
    }));
    expect(() =>
      evaluate(networkLoadBalancer, loadBalancerInput(networkLoadBalancer))
    ).toThrow('not an ALB');
  });

  it('複数 target を安定順で選び target subnet の VPC 不整合を拒否する', async () => {
    const multipleTargets = await networkContext();
    const original = resourceByLogicalId(multipleTargets, 'TargetInstance');
    const backupId = `${reference(multipleTargets, 'TargetInstance')}-backup`;
    saveResource(multipleTargets, {
      ...original,
      resourceId: `${original.resourceId}-backup`,
      properties: {
        ...original.properties,
        logicalId: 'BackupTargetInstance',
        physicalId: backupId,
        refValue: backupId,
      },
    });
    updateTemplate(multipleTargets, 'TargetGroup', (template) => ({
      ...template,
      Targets: [
        { Id: backupId },
        { Id: reference(multipleTargets, 'TargetInstance') },
      ],
    }));
    expect(
      evaluate(multipleTargets, loadBalancerInput(multipleTargets))
    ).toMatchObject({ reachable: true, reasons: [] });
    updateState(multipleTargets, 'TargetInstance', {
      instanceState: 'stopped',
    });
    updateState(multipleTargets, 'BackupTargetInstance', {
      instanceState: 'stopped',
    });
    const stoppedTargets = evaluate(
      multipleTargets,
      loadBalancerInput(multipleTargets)
    );
    expect(stoppedTargets).toMatchObject({
      reachable: false,
      reasons: ['TARGET_INSTANCE_NOT_RUNNING'],
    });
    expect(
      pathTypes(stoppedTargets).filter((type) => type === INSTANCE_RESOURCE)
    ).toHaveLength(2);

    const mixedVpc = await networkContext();
    const mixedSubnetId = addOtherVpcSubnet(mixedVpc);
    updateTemplate(mixedVpc, 'LoadBalancer', (template) => ({
      ...template,
      Subnets: [reference(mixedVpc, 'PublicSubnet'), mixedSubnetId],
    }));
    expect(() => evaluate(mixedVpc, loadBalancerInput(mixedVpc))).toThrow(
      'load balancer subnets belong to different VPCs'
    );

    const otherVpc = await networkContext();
    const otherSubnetId = addOtherVpcSubnet(otherVpc);
    updateTemplate(otherVpc, 'TargetInstance', (template) => ({
      ...template,
      SubnetId: otherSubnetId,
    }));
    expect(() => evaluate(otherVpc, loadBalancerInput(otherVpc))).toThrow(
      'target instance belongs to a different VPC'
    );
  });

  it('CIDR、protocol、port、destination、resource type の曖昧な入力を strict に拒否する', async () => {
    const context = await networkContext();
    const instanceId = reference(context, 'DirectInstance');
    const loadBalancerArn = reference(context, 'LoadBalancer');
    const base = {
      SourceCidr: '203.0.113.0/24',
      IpProtocol: 'tcp',
      Port: 80,
      DestinationInstanceId: instanceId,
    };
    const invalid = [
      { ...base, SourceCidr: '203.0.113.1/24' },
      { ...base, SourceCidr: '300.0.0.0/24' },
      { ...base, SourceCidr: '2001:db8::/64' },
      { ...base, IpProtocol: 'TCP' },
      { ...base, IpProtocol: 'icmp' },
      { ...base, Port: 0 },
      { ...base, Port: 1.5 },
      { ...base, DestinationInstanceId: '' },
      {
        ...base,
        DestinationLoadBalancerArn: loadBalancerArn,
      },
      {
        SourceCidr: base.SourceCidr,
        IpProtocol: base.IpProtocol,
        Port: base.Port,
      },
      { ...base, Unknown: true },
    ];
    for (const input of invalid) {
      expect(() => evaluate(context, input)).toThrow();
    }
    expect(() => evaluateDirect(context, base, '*')).toThrow(
      'AWS::EC2::Instance'
    );
    expect(() =>
      evaluate(context, { ...base, DestinationInstanceId: 'i-missing' })
    ).toThrow('destination instance does not exist');
  });

  it('重複 association、複数 forward action、解釈不能な WAF rule を loud に拒否する', async () => {
    const duplicateContext = await networkContext();
    const association = resourceByLogicalId(
      duplicateContext,
      'PublicRouteAssociation'
    );
    saveResource(duplicateContext, {
      ...association,
      resourceId: `${association.resourceId}-duplicate`,
      properties: {
        ...association.properties,
        logicalId: 'DuplicatePublicRouteAssociation',
        physicalId: `${association.properties['physicalId']}-duplicate`,
      },
    });
    expect(() =>
      evaluate(duplicateContext, {
        SourceCidr: '203.0.113.0/24',
        IpProtocol: 'tcp',
        Port: 80,
        DestinationInstanceId: reference(duplicateContext, 'DirectInstance'),
      })
    ).toThrow('route-table association is ambiguous');

    const actionContext = await networkContext();
    updateProperties(actionContext, 'Listener', (properties) => {
      const template = properties['templateProperties'];
      if (template === null || typeof template !== 'object') {
        throw new Error('listener template is missing');
      }
      return {
        ...properties,
        templateProperties: {
          ...template,
          DefaultActions: [
            {
              Type: 'forward',
              TargetGroupArn: reference(actionContext, 'TargetGroup'),
            },
            {
              Type: 'forward',
              TargetGroupArn: reference(actionContext, 'TargetGroup'),
            },
          ],
        },
      };
    });
    expect(() =>
      evaluate(actionContext, {
        SourceCidr: '198.51.100.0/24',
        IpProtocol: 'tcp',
        Port: 443,
        DestinationLoadBalancerArn: reference(actionContext, 'LoadBalancer'),
      })
    ).toThrow('listener forward action is ambiguous');

    const wafContext = await networkContext();
    const loadBalancerArn = reference(wafContext, 'LoadBalancer');
    updateProperties(wafContext, 'AllowWebAcl', (properties) => {
      const template = properties['templateProperties'];
      if (template === null || typeof template !== 'object') {
        throw new Error('Web ACL template is missing');
      }
      return {
        ...properties,
        templateProperties: {
          ...template,
          Rules: [{ Name: 'request-dependent-rule', Priority: 1 }],
        },
      };
    });
    execute(wafContext, 'wafv2', 'AssociateWebACL', {
      WebACLArn: String(attributes(wafContext, 'AllowWebAcl')['Arn']),
      ResourceArn: loadBalancerArn,
    });
    expect(() =>
      evaluate(wafContext, {
        SourceCidr: '198.51.100.0/24',
        IpProtocol: 'tcp',
        Port: 443,
        DestinationLoadBalancerArn: loadBalancerArn,
      })
    ).toThrow('WAF rules require HTTP request context');
  });
});
