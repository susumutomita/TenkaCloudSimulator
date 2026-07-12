import {
  CoreError,
  type ProviderClockInput,
  type ProviderClockResult,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderCompileInput,
  type ProviderDeploymentResult,
  type ProviderModule,
  type ProviderTargetPlan,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import { compileCloudFormation } from './cloudformation';
import { reduceCloudFormation } from './cloudformation-state';
import { deployCloudFormation } from './deploy';
import { reduceEc2 } from './ec2';
import { reduceElb } from './elb';
import {
  type ExternalEvaluatorOptions,
  validatedTrustedWorkerOrigins,
} from './external-evaluator';
import { reduceHttp } from './http';
import { reduceHttpProbe } from './http-probe';
import { reduceIam } from './iam';
import { reduceLambda, reduceLambdaAsync } from './lambda';
import { reduceLogs } from './logs';
import { AWS_CAPABILITIES, AWS_PROVIDER, CLOUDFORMATION_ENGINE } from './model';
import { reduceRds } from './rds';
import { reduceRuntime } from './runtime';
import { reduceS3 } from './s3';
import { advanceSsmClock, reduceSsm } from './ssm';
import { reduceSts } from './sts';
import { reduceWaf } from './waf';

export class AwsProvider implements ProviderModule {
  readonly provider: string;
  readonly engines: readonly string[];
  readonly capabilities: typeof AWS_CAPABILITIES;
  readonly #trustedWorkerOrigins: ReadonlySet<string>;

  constructor(options: ExternalEvaluatorOptions = {}) {
    this.provider = AWS_PROVIDER;
    this.engines = [CLOUDFORMATION_ENGINE];
    this.capabilities = AWS_CAPABILITIES;
    this.#trustedWorkerOrigins = validatedTrustedWorkerOrigins(
      options.trustedWorkerOrigins
    );
  }

  compile(input: ProviderCompileInput): ProviderTargetPlan {
    if (input.target.engine !== CLOUDFORMATION_ENGINE) {
      throw new CoreError(
        'UnsupportedCapability',
        `AWS engine ${input.target.engine} is not supported`
      );
    }
    return compileCloudFormation(input);
  }

  deploy(
    plan: ProviderTargetPlan,
    world: ProviderWorldView
  ): ProviderDeploymentResult {
    return deployCloudFormation(plan, world);
  }

  reduce(
    command: ProviderCommandInput,
    world: ProviderWorldView
  ): ProviderCommandResult {
    switch (command.service) {
      case 'cloudformation':
        return reduceCloudFormation(command, world);
      case 'iam':
        return reduceIam(command, world);
      case 'ssm':
        return reduceSsm(command, world);
      case 's3':
        return reduceS3(command, world);
      case 'lambda':
        return reduceLambda(command, world);
      case 'elasticloadbalancing':
        return reduceElb(command, world);
      case 'ec2':
        return reduceEc2(command, world);
      case 'rds':
        return reduceRds(command, world);
      case 'wafv2':
        return reduceWaf(command, world);
      case 'logs':
        return reduceLogs(command, world);
      case 'sts':
        return reduceSts(command, world);
      case 'runtime':
        return reduceRuntime(command, world);
      case 'http':
        return reduceHttp(command, world);
      default:
        throw new CoreError(
          'UnsupportedCapability',
          `AWS service ${command.service} is not supported`
        );
    }
  }

  async reduceAsync(
    command: ProviderCommandInput,
    world: ProviderWorldView
  ): Promise<ProviderCommandResult> {
    if (command.service === 'lambda') {
      return reduceLambdaAsync(command, world, this.#trustedWorkerOrigins);
    }
    if (
      command.service === 'http' &&
      (command.operation === 'Probe' || command.operation === 'Poll')
    ) {
      return reduceHttpProbe(command);
    }
    return this.reduce(command, world);
  }

  advanceClock(
    input: ProviderClockInput,
    world: ProviderWorldView
  ): ProviderClockResult {
    return advanceSsmClock(input, world);
  }
}
