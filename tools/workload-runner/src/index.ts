import { createHash } from 'node:crypto';

const PINNED_IMAGE =
  /^(?:[a-z0-9][a-z0-9._/-]*\/)?[a-z0-9][a-z0-9._/-]*@sha256:[a-f0-9]{64}$/;
const SAFE_ENVIRONMENT_KEY = /^[A-Z][A-Z0-9_]{0,63}$/;
const SENSITIVE_ENVIRONMENT_KEY =
  /(?:ACCESS_KEY|CREDENTIAL|PASSWORD|PRIVATE_KEY|SECRET|TOKEN)/;
const MAX_ENVIRONMENT_VALUE_LENGTH = 4096;
const DEFAULT_DOCKER_TIMEOUT_MILLISECONDS = 30_000;
const CONTAINER_REMOVAL_ATTEMPTS = 40;
const CONTAINER_REMOVAL_INTERVAL_MILLISECONDS = 50;
const MAX_WORKLOADS_PER_MATERIALIZATION = 32;
const WORLD_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/;
const WORKLOAD_ID = /^[a-z][a-z0-9-]{0,63}$/;
const TARGET_ID = /^(?:default|[a-z][a-z0-9-]{0,31})$/;
const RESOURCE_REFERENCE = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$/;
const HEALTH_PATH = /^\/(?!\/)[^?#\s]{0,255}$/;
const CONTROL_CONTAINER_NAME = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/;
const HEX_CONTAINER_SELECTOR = /^[a-f0-9]+$/;
const FULL_CONTAINER_ID = /^[a-f0-9]{64}$/;
const DECLARATION_FIELDS = new Set([
  'id',
  'targetId',
  'resourceRef',
  'image',
  'command',
  'containerPort',
  'healthPath',
]);

export type WorkloadRunnerErrorCode =
  | 'InvalidWorkload'
  | 'RunnerUnavailable'
  | 'WorkloadFailed'
  | 'WorkloadNotFound';

export class WorkloadRunnerError extends Error {
  constructor(
    readonly code: WorkloadRunnerErrorCode,
    message: string
  ) {
    super(message);
    this.name = 'WorkloadRunnerError';
  }
}

export interface WorkloadPolicy {
  readonly allowedImages: ReadonlySet<string>;
  readonly proxyImage: string;
  readonly maxMemoryBytes: number;
  readonly maxMilliCpu: number;
  readonly maxPids: number;
  readonly controlContainer?: string;
}

export interface WorkloadSpec {
  readonly worldId: string;
  readonly workloadId: string;
  readonly image: string;
  readonly command?: readonly string[];
  readonly environment?: Readonly<Record<string, string>>;
  readonly containerPort?: number;
  readonly memoryBytes?: number;
  readonly milliCpu?: number;
  readonly pids?: number;
  readonly targetId?: string;
  readonly resourceRef?: string;
  readonly healthPath?: string;
}

export interface WorkloadDeclaration {
  readonly id: string;
  readonly targetId: string;
  readonly resourceRef: string;
  readonly image: string;
  readonly command?: readonly string[];
  readonly containerPort: number;
  readonly healthPath?: string;
}

export interface RunningWorkload {
  readonly containerId: string;
  readonly endpoint?: string;
  readonly networkName: string;
  readonly proxyContainerId?: string;
}

export interface WorkloadInspection {
  readonly containerId: string;
  readonly image: string;
  readonly running: boolean;
  readonly user: string;
  readonly readOnlyRootFilesystem: boolean;
  readonly droppedCapabilities: readonly string[];
  readonly securityOptions: readonly string[];
  readonly memoryBytes: number;
  readonly nanoCpus: number;
  readonly pids: number;
  readonly networkName: string;
  readonly endpoint?: string;
  readonly role: string;
  readonly specHash: string;
  readonly workloadId: string;
  readonly worldId: string;
  readonly targetId: string;
  readonly resourceRef: string;
  readonly healthPath: string;
  readonly containerPort: number;
}

export interface MaterializedWorkload extends RunningWorkload {
  readonly worldId: string;
  readonly workloadId: string;
  readonly targetId: string;
  readonly resourceRef: string;
  readonly image: string;
  readonly healthPath: string;
}

export interface WorkloadProbeResult {
  readonly endpoint: string;
  readonly healthPath: string;
  readonly healthy: boolean;
  readonly status: number;
}

interface DockerResult {
  readonly exitCode: number;
  readonly stdout: string;
  readonly stderr: string;
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function requiredRecord(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> {
  if (!isRecord(value)) {
    throw new WorkloadRunnerError(
      'WorkloadFailed',
      `Docker returned an invalid ${label}`
    );
  }
  return value;
}

function requiredText(value: string, label: string): void {
  if (!value.trim() || value.length > 256) {
    throw new WorkloadRunnerError(
      'InvalidWorkload',
      `${label} must contain between 1 and 256 characters`
    );
  }
}

function invalidWorkload(message: string): never {
  throw new WorkloadRunnerError('InvalidWorkload', message);
}

function declarationText(
  declaration: Readonly<Record<string, unknown>>,
  key: string
): string {
  const value = declaration[key];
  if (typeof value !== 'string') {
    invalidWorkload(`workload declaration ${key} must be a string`);
  }
  return value;
}

function declarationCommand(value: unknown): readonly string[] | undefined {
  if (value === undefined) return undefined;
  if (
    !Array.isArray(value) ||
    value.length < 1 ||
    value.length > 32 ||
    value.some(
      (argument) =>
        typeof argument !== 'string' ||
        argument.length < 1 ||
        argument.length > 512 ||
        argument.includes('\u0000')
    )
  ) {
    invalidWorkload(
      'workload declaration command must contain 1 to 32 bounded arguments'
    );
  }
  return value;
}

function declarationPort(value: unknown): number {
  if (
    typeof value !== 'number' ||
    !Number.isSafeInteger(value) ||
    value < 1024 ||
    value > 65_535
  ) {
    invalidWorkload(
      'workload declaration containerPort must be an unprivileged TCP port'
    );
  }
  return value;
}

function parseDeclaration(value: unknown): WorkloadDeclaration {
  if (!isRecord(value)) {
    invalidWorkload('workload declaration must be an object');
  }
  const unknownField = Object.keys(value).find(
    (field) => !DECLARATION_FIELDS.has(field)
  );
  if (unknownField) {
    invalidWorkload(`workload declaration field ${unknownField} is unknown`);
  }
  const command = declarationCommand(value['command']);
  const healthPath = value['healthPath'];
  if (healthPath !== undefined && typeof healthPath !== 'string') {
    invalidWorkload('workload declaration healthPath must be a string');
  }
  return {
    id: declarationText(value, 'id'),
    targetId: declarationText(value, 'targetId'),
    resourceRef: declarationText(value, 'resourceRef'),
    image: declarationText(value, 'image'),
    containerPort: declarationPort(value['containerPort']),
    ...(command === undefined ? {} : { command }),
    ...(healthPath === undefined ? {} : { healthPath }),
  };
}

function parseDeclarations(value: unknown): readonly WorkloadDeclaration[] {
  if (
    !Array.isArray(value) ||
    value.length < 1 ||
    value.length > MAX_WORKLOADS_PER_MATERIALIZATION
  ) {
    invalidWorkload(
      `workload declarations must contain 1 to ${MAX_WORKLOADS_PER_MATERIALIZATION} items`
    );
  }
  const declarations = value.map(parseDeclaration);
  const ids = new Set<string>();
  for (const declaration of declarations) {
    if (ids.has(declaration.id)) {
      invalidWorkload(
        `workload declaration ID ${declaration.id} is duplicated`
      );
    }
    ids.add(declaration.id);
  }
  return declarations.sort((left, right) => left.id.localeCompare(right.id));
}

function validateWorkloadIdentity(spec: WorkloadSpec): void {
  requiredText(spec.worldId, 'worldId');
  requiredText(spec.workloadId, 'workloadId');
  if (!WORLD_ID.test(spec.worldId)) {
    invalidWorkload('worldId must use the simulator world identifier format');
  }
  if (!WORKLOAD_ID.test(spec.workloadId)) {
    invalidWorkload(
      'workloadId must use the simulator workload identifier format'
    );
  }
  if (spec.targetId !== undefined && !TARGET_ID.test(spec.targetId)) {
    invalidWorkload('targetId must use the simulator target identifier format');
  }
  if (
    spec.resourceRef !== undefined &&
    !RESOURCE_REFERENCE.test(spec.resourceRef)
  ) {
    invalidWorkload(
      'resourceRef must be a bounded simulator resource reference'
    );
  }
  if (spec.healthPath !== undefined && !HEALTH_PATH.test(spec.healthPath)) {
    invalidWorkload('healthPath must be an origin-relative HTTP path');
  }
}

function validateWorkloadCommand(command: readonly string[] | undefined): void {
  if (
    command !== undefined &&
    (command.length < 1 ||
      command.length > 32 ||
      command.some(
        (argument) =>
          argument.length < 1 ||
          argument.length > 512 ||
          argument.includes('\u0000')
      ))
  ) {
    invalidWorkload('workload command must contain 1 to 32 bounded arguments');
  }
}

function workloadLabels(
  spec: WorkloadSpec,
  role: 'proxy' | 'workload'
): readonly string[] {
  return [
    `--label=tenkacloud.simulator.world=${spec.worldId}`,
    `--label=tenkacloud.simulator.workload=${spec.workloadId}`,
    `--label=tenkacloud.simulator.role=${role}`,
    `--label=tenkacloud.simulator.target=${spec.targetId ?? ''}`,
    `--label=tenkacloud.simulator.resource=${spec.resourceRef ?? ''}`,
    `--label=tenkacloud.simulator.health=${spec.healthPath ?? '/'}`,
    `--label=tenkacloud.simulator.port=${spec.containerPort ?? ''}`,
  ];
}

function runningWorkload(
  inspection: WorkloadInspection,
  networkName: string,
  proxy: WorkloadInspection | undefined
): RunningWorkload {
  return {
    containerId: inspection.containerId,
    networkName,
    ...(proxy?.endpoint === undefined ? {} : { endpoint: proxy.endpoint }),
    ...(proxy === undefined ? {} : { proxyContainerId: proxy.containerId }),
  };
}

async function fetchOptional(
  input: string,
  init: RequestInit
): Promise<Response | undefined> {
  try {
    return await fetch(input, init);
  } catch {
    return undefined;
  }
}

function boundedInteger(
  requested: number | undefined,
  maximum: number,
  label: string
): number {
  const value = requested ?? maximum;
  if (!Number.isSafeInteger(value) || value < 1 || value > maximum) {
    throw new WorkloadRunnerError(
      'InvalidWorkload',
      `${label} must be an integer between 1 and ${maximum}`
    );
  }
  return value;
}

function environmentArguments(
  environment: Readonly<Record<string, string>> | undefined
): string[] {
  const args: string[] = [];
  for (const [key, value] of Object.entries(environment ?? {}).sort(
    ([left], [right]) => left.localeCompare(right)
  )) {
    if (
      !SAFE_ENVIRONMENT_KEY.test(key) ||
      SENSITIVE_ENVIRONMENT_KEY.test(key) ||
      value.length > MAX_ENVIRONMENT_VALUE_LENGTH ||
      value.includes('\u0000')
    ) {
      throw new WorkloadRunnerError(
        'InvalidWorkload',
        `environment variable ${key} violates the workload policy`
      );
    }
    args.push(`--env=${key}=${value}`);
  }
  return args;
}

function identifier(prefix: string, ...values: readonly string[]): string {
  const digest = createHash('sha256')
    .update(values.join('\u0000'))
    .digest('hex');
  return `${prefix}-${digest.slice(0, 20)}`;
}

function safeDockerMessage(value: string): string {
  const message = value.trim().replaceAll(/\s+/g, ' ');
  return message.length > 500 ? `${message.slice(0, 500)}…` : message;
}

function stringArray(value: unknown): readonly string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string')
    : [];
}

function stringValue(
  record: Readonly<Record<string, unknown>>,
  key: string
): string {
  const value = record[key];
  return typeof value === 'string' ? value : '';
}

function numberValue(
  record: Readonly<Record<string, unknown>>,
  key: string
): number {
  const value = record[key];
  return typeof value === 'number' ? value : 0;
}

function loopbackEndpoint(
  ports: Readonly<Record<string, unknown>>
): string | undefined {
  const bindings = Object.values(ports).flatMap((value) =>
    Array.isArray(value) ? value : []
  );
  for (const binding of bindings) {
    if (!isRecord(binding)) continue;
    const hostIp = stringValue(binding, 'HostIp');
    const hostPort = stringValue(binding, 'HostPort');
    if ((hostIp === '127.0.0.1' || hostIp === '::1') && hostPort) {
      return `http://127.0.0.1:${hostPort}`;
    }
  }
  return undefined;
}

function singleInspection(output: string): Readonly<Record<string, unknown>> {
  const parsed: unknown = JSON.parse(output);
  if (!Array.isArray(parsed) || parsed.length !== 1) {
    throw new WorkloadRunnerError(
      'WorkloadFailed',
      'Docker returned an invalid inspection response'
    );
  }
  return requiredRecord(parsed[0], 'container inspection');
}

export class DockerWorkloadRunner {
  constructor(
    readonly policy: WorkloadPolicy,
    readonly dockerBinary = 'docker',
    readonly dockerTimeoutMilliseconds = DEFAULT_DOCKER_TIMEOUT_MILLISECONDS
  ) {
    if (
      policy.allowedImages.size < 1 ||
      !PINNED_IMAGE.test(policy.proxyImage) ||
      !policy.allowedImages.has(policy.proxyImage) ||
      !Number.isSafeInteger(policy.maxMemoryBytes) ||
      policy.maxMemoryBytes < 1 ||
      !Number.isSafeInteger(policy.maxMilliCpu) ||
      policy.maxMilliCpu < 1 ||
      !Number.isSafeInteger(policy.maxPids) ||
      policy.maxPids < 1 ||
      (policy.controlContainer !== undefined &&
        !FULL_CONTAINER_ID.test(policy.controlContainer) &&
        (HEX_CONTAINER_SELECTOR.test(policy.controlContainer) ||
          !CONTROL_CONTAINER_NAME.test(policy.controlContainer))) ||
      !Number.isSafeInteger(dockerTimeoutMilliseconds) ||
      dockerTimeoutMilliseconds < 1
    ) {
      throw new WorkloadRunnerError(
        'InvalidWorkload',
        'workload policy limits and image allowlist must be non-empty'
      );
    }
  }

  async #docker(args: readonly string[]): Promise<DockerResult> {
    let process: Bun.ReadableSubprocess;
    const signal = AbortSignal.timeout(this.dockerTimeoutMilliseconds);
    try {
      process = Bun.spawn([this.dockerBinary, ...args], {
        stdout: 'pipe',
        stderr: 'pipe',
        signal,
      });
    } catch {
      throw new WorkloadRunnerError(
        'RunnerUnavailable',
        'Docker executable is unavailable'
      );
    }
    const [exitCode, stdout, stderr] = await Promise.all([
      process.exited,
      new Response(process.stdout).text(),
      new Response(process.stderr).text(),
    ]);
    if (signal.aborted) {
      throw new WorkloadRunnerError(
        'RunnerUnavailable',
        'Docker did not answer within the workload timeout'
      );
    }
    return { exitCode, stdout, stderr };
  }

  async available(): Promise<boolean> {
    const result = await this.#docker([
      'version',
      '--format',
      '{{.Server.Version}}',
    ]);
    return result.exitCode === 0 && result.stdout.trim().length > 0;
  }

  async #requiredDocker(
    args: readonly string[],
    action: string
  ): Promise<string> {
    const result = await this.#docker(args);
    if (result.exitCode !== 0) {
      throw new WorkloadRunnerError(
        'WorkloadFailed',
        `${action} failed: ${safeDockerMessage(result.stderr) || 'Docker returned no diagnostic'}`
      );
    }
    return result.stdout.trim();
  }

  async #waitForContainerRemoval(container: string): Promise<boolean> {
    for (let attempt = 0; attempt < CONTAINER_REMOVAL_ATTEMPTS; attempt += 1) {
      const inspection = await this.#docker([
        'container',
        'inspect',
        container,
      ]);
      if (inspection.exitCode !== 0) return true;
      await Bun.sleep(CONTAINER_REMOVAL_INTERVAL_MILLISECONDS);
    }
    return false;
  }

  async #stopAndAwaitRemoval(container: string): Promise<boolean> {
    const stopped = await this.#docker([
      'container',
      'stop',
      '--time=1',
      container,
    ]);
    if (stopped.exitCode !== 0) {
      const stillExists = await this.#docker([
        'container',
        'inspect',
        container,
      ]);
      return stillExists.exitCode !== 0;
    }
    return this.#waitForContainerRemoval(container);
  }

  #networkName(worldId: string): string {
    return identifier('tc-sim-net', worldId);
  }

  #containerName(spec: WorkloadSpec): string {
    return identifier('tc-sim-workload', spec.worldId, spec.workloadId);
  }

  #specHash(spec: WorkloadSpec): string {
    const environment = Object.fromEntries(
      Object.entries(spec.environment ?? {}).sort(([left], [right]) =>
        left.localeCompare(right)
      )
    );
    return createHash('sha256')
      .update(
        JSON.stringify({
          image: spec.image,
          command: spec.command ?? [],
          environment,
          containerPort: spec.containerPort ?? null,
          memoryBytes: spec.memoryBytes ?? null,
          milliCpu: spec.milliCpu ?? null,
          pids: spec.pids ?? null,
          targetId: spec.targetId ?? null,
          resourceRef: spec.resourceRef ?? null,
          healthPath: spec.healthPath ?? '/',
        })
      )
      .digest('hex');
  }

  #proxyName(spec: WorkloadSpec): string {
    return identifier('tc-sim-proxy', spec.worldId, spec.workloadId);
  }

  async #resolvedControlContainerId(): Promise<string | undefined> {
    const configured = this.policy.controlContainer;
    if (configured === undefined) return undefined;
    const output = await this.#requiredDocker(
      ['container', 'inspect', configured],
      'control container inspection'
    );
    const root = singleInspection(output);
    const resolved = stringValue(root, 'Id');
    if (!FULL_CONTAINER_ID.test(resolved)) {
      throw new WorkloadRunnerError(
        'WorkloadFailed',
        'Docker resolved an invalid control container identity'
      );
    }
    return resolved;
  }

  async #networkContainers(
    networkName: string
  ): Promise<
    readonly { readonly containerId: string; readonly name: string }[]
  > {
    const output = await this.#requiredDocker(
      ['network', 'inspect', networkName, '--format={{json .Containers}}'],
      'workload network membership inspection'
    );
    let parsed: unknown;
    try {
      parsed = JSON.parse(output);
    } catch {
      throw new WorkloadRunnerError(
        'WorkloadFailed',
        'Docker returned invalid workload network membership'
      );
    }
    if (parsed === null) return [];
    const memberships = requiredRecord(parsed, 'network membership');
    return Object.entries(memberships)
      .map(([containerId, value]) => {
        const membership = requiredRecord(
          value,
          'network container membership'
        );
        const name = stringValue(membership, 'Name');
        if (!FULL_CONTAINER_ID.test(containerId) || !name) {
          throw new WorkloadRunnerError(
            'WorkloadFailed',
            'Docker returned an invalid workload network container identity'
          );
        }
        return { containerId, name };
      })
      .sort((left, right) => left.containerId.localeCompare(right.containerId));
  }

  async #networkContainsContainer(
    networkName: string,
    containerId: string
  ): Promise<boolean> {
    for (const container of await this.#networkContainers(networkName)) {
      if (container.containerId === containerId) return true;
    }
    return false;
  }

  async #ensureControlContainer(networkName: string): Promise<void> {
    const controlContainerId = await this.#resolvedControlContainerId();
    if (controlContainerId === undefined) return;
    if (await this.#networkContainsContainer(networkName, controlContainerId)) {
      return;
    }
    const connected = await this.#docker([
      'network',
      'connect',
      networkName,
      controlContainerId,
    ]);
    if (connected.exitCode === 0) return;
    if (await this.#networkContainsContainer(networkName, controlContainerId)) {
      return;
    }
    throw new WorkloadRunnerError(
      'WorkloadFailed',
      `control container network attachment failed: ${safeDockerMessage(connected.stderr) || 'Docker returned no diagnostic'}`
    );
  }

  async #disconnectControlContainer(networkName: string): Promise<void> {
    const configured = this.policy.controlContainer;
    if (configured === undefined) return;
    const matches = (await this.#networkContainers(networkName)).filter(
      (container) =>
        FULL_CONTAINER_ID.test(configured)
          ? container.containerId === configured
          : container.name === configured
    );
    if (matches.length > 1) {
      throw new WorkloadRunnerError(
        'WorkloadFailed',
        'workload network contains an ambiguous control container identity'
      );
    }
    const controlContainer = matches[0];
    if (controlContainer === undefined) return;
    const disconnected = await this.#docker([
      'network',
      'disconnect',
      networkName,
      controlContainer.containerId,
    ]);
    if (disconnected.exitCode === 0) return;
    if (
      !(await this.#networkContainsContainer(
        networkName,
        controlContainer.containerId
      ))
    ) {
      return;
    }
    throw new WorkloadRunnerError(
      'WorkloadFailed',
      `control container network detachment failed: ${safeDockerMessage(disconnected.stderr) || 'Docker returned no diagnostic'}`
    );
  }

  async #ensureNetwork(worldId: string): Promise<string> {
    const networkName = this.#networkName(worldId);
    const inspected = await this.#docker(['network', 'inspect', networkName]);
    if (inspected.exitCode !== 0) {
      const created = await this.#docker([
        'network',
        'create',
        '--driver=bridge',
        '--internal',
        `--label=tenkacloud.simulator.world=${worldId}`,
        networkName,
      ]);
      if (created.exitCode !== 0) {
        const raced = await this.#docker(['network', 'inspect', networkName]);
        if (raced.exitCode !== 0) {
          throw new WorkloadRunnerError(
            'WorkloadFailed',
            `workload network creation failed: ${safeDockerMessage(created.stderr)}`
          );
        }
      }
    }
    await this.#ensureControlContainer(networkName);
    return networkName;
  }

  #healthEndpoint(
    spec: WorkloadSpec,
    proxyName: string,
    loopbackEndpoint: string
  ): string {
    if (this.policy.controlContainer === undefined) return loopbackEndpoint;
    const port = spec.containerPort;
    if (port === undefined) {
      throw new WorkloadRunnerError(
        'InvalidWorkload',
        'internal workload health requires a containerPort'
      );
    }
    return `http://${proxyName}:${port}`;
  }

  #validate(spec: WorkloadSpec): {
    readonly memoryBytes: number;
    readonly milliCpu: number;
    readonly pids: number;
  } {
    validateWorkloadIdentity(spec);
    if (!PINNED_IMAGE.test(spec.image)) {
      throw new WorkloadRunnerError(
        'InvalidWorkload',
        'workload image must be pinned by sha256 digest'
      );
    }
    if (!this.policy.allowedImages.has(spec.image)) {
      throw new WorkloadRunnerError(
        'InvalidWorkload',
        'workload image is not present in the allowlist'
      );
    }
    if (
      spec.containerPort !== undefined &&
      (!Number.isSafeInteger(spec.containerPort) ||
        spec.containerPort < 1024 ||
        spec.containerPort > 65_535)
    ) {
      throw new WorkloadRunnerError(
        'InvalidWorkload',
        'containerPort must be an unprivileged TCP port'
      );
    }
    validateWorkloadCommand(spec.command);
    environmentArguments(spec.environment);
    return {
      memoryBytes: boundedInteger(
        spec.memoryBytes,
        this.policy.maxMemoryBytes,
        'memoryBytes'
      ),
      milliCpu: boundedInteger(
        spec.milliCpu,
        this.policy.maxMilliCpu,
        'milliCpu'
      ),
      pids: boundedInteger(spec.pids, this.policy.maxPids, 'pids'),
    };
  }

  async start(spec: WorkloadSpec): Promise<RunningWorkload> {
    const limits = this.#validate(spec);
    const networkName = await this.#ensureNetwork(spec.worldId);
    const containerName = this.#containerName(spec);
    const existing = await this.#docker([
      'container',
      'inspect',
      containerName,
    ]);
    if (existing.exitCode === 0) {
      const inspection = await this.inspect(containerName);
      if (
        inspection.image !== spec.image ||
        inspection.specHash !== this.#specHash(spec) ||
        !inspection.running
      ) {
        throw new WorkloadRunnerError(
          'WorkloadFailed',
          'workload identity is occupied by a different container state'
        );
      }
      const proxy =
        spec.containerPort === undefined
          ? undefined
          : await this.#ensureProxy(spec, containerName, networkName);
      return runningWorkload(inspection, networkName, proxy);
    }
    const args = [
      'run',
      '--detach',
      '--rm',
      `--name=${containerName}`,
      `--label=tenkacloud.simulator.spec=${this.#specHash(spec)}`,
      ...workloadLabels(spec, 'workload'),
      '--user=65532:65532',
      '--read-only',
      '--cap-drop=ALL',
      '--security-opt=no-new-privileges:true',
      `--memory=${limits.memoryBytes}b`,
      `--cpus=${limits.milliCpu / 1000}`,
      `--pids-limit=${limits.pids}`,
      `--network=${networkName}`,
      '--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=16777216',
      ...environmentArguments(spec.environment),
      spec.image,
      ...(spec.command ?? []),
    ];
    const containerId = await this.#requiredDocker(args, 'workload start');
    const inspection = await this.inspect(containerId);
    let proxy: WorkloadInspection | undefined;
    try {
      proxy =
        spec.containerPort === undefined
          ? undefined
          : await this.#ensureProxy(spec, containerName, networkName);
    } catch (error) {
      await this.#stopAndAwaitRemoval(containerId);
      throw error;
    }
    return runningWorkload(inspection, networkName, proxy);
  }

  async #ensureProxy(
    spec: WorkloadSpec,
    workloadName: string,
    networkName: string
  ): Promise<WorkloadInspection> {
    const port = spec.containerPort;
    if (port === undefined) {
      throw new WorkloadRunnerError(
        'InvalidWorkload',
        'proxy requires a containerPort'
      );
    }
    const proxyName = this.#proxyName(spec);
    const existing = await this.#docker(['container', 'inspect', proxyName]);
    if (existing.exitCode === 0) {
      const inspection = await this.inspect(proxyName);
      if (
        !inspection.running ||
        !inspection.endpoint ||
        inspection.image !== this.policy.proxyImage ||
        inspection.role !== 'proxy' ||
        inspection.worldId !== spec.worldId ||
        inspection.workloadId !== spec.workloadId ||
        inspection.specHash !== this.#specHash(spec)
      ) {
        throw new WorkloadRunnerError(
          'WorkloadFailed',
          'workload proxy is not in a reusable state'
        );
      }
      await this.#waitForEndpoint(
        this.#healthEndpoint(spec, proxyName, inspection.endpoint),
        spec.healthPath ?? '/'
      );
      return inspection;
    }
    const proxyScript = [
      `printf '%s\\n' '#!/bin/sh' 'exec nc ${workloadName} ${port}' > /tmp/tenkacloud-proxy`,
      'chmod 500 /tmp/tenkacloud-proxy',
      `exec nc -lk -p ${port} -e /tmp/tenkacloud-proxy`,
    ].join('; ');
    const proxyId = await this.#requiredDocker(
      [
        'run',
        '--detach',
        '--rm',
        `--name=${proxyName}`,
        ...workloadLabels(spec, 'proxy'),
        `--label=tenkacloud.simulator.spec=${this.#specHash(spec)}`,
        '--user=65532:65532',
        '--read-only',
        '--cap-drop=ALL',
        '--security-opt=no-new-privileges:true',
        '--memory=33554432b',
        '--cpus=0.1',
        '--pids-limit=16',
        '--network=bridge',
        '--tmpfs=/tmp:rw,exec,nosuid,nodev,size=1048576',
        `--publish=127.0.0.1::${port}`,
        this.policy.proxyImage,
        'sh',
        '-c',
        proxyScript,
      ],
      'workload proxy start'
    );
    try {
      await this.#requiredDocker(
        ['network', 'connect', networkName, proxyId],
        'workload proxy network attachment'
      );
      const inspection = await this.inspect(proxyId);
      if (!inspection.endpoint) {
        throw new WorkloadRunnerError(
          'WorkloadFailed',
          'workload proxy did not publish a loopback endpoint'
        );
      }
      await this.#waitForEndpoint(
        this.#healthEndpoint(spec, proxyName, inspection.endpoint),
        spec.healthPath ?? '/'
      );
      return inspection;
    } catch (error) {
      await this.#stopAndAwaitRemoval(proxyId);
      throw error;
    }
  }

  async #waitForEndpoint(endpoint: string, healthPath: string): Promise<void> {
    for (let attempt = 0; attempt < 20; attempt += 1) {
      const response = await fetchOptional(`${endpoint}${healthPath}`, {
        redirect: 'manual',
        signal: AbortSignal.timeout(1000),
      });
      if (response && response.status >= 200 && response.status < 300) {
        await response.body?.cancel();
        return;
      }
      await response?.body?.cancel();
      await Bun.sleep(50);
    }
    throw new WorkloadRunnerError(
      'WorkloadFailed',
      'workload proxy did not become reachable'
    );
  }

  async inspect(container: string): Promise<WorkloadInspection> {
    const output = await this.#requiredDocker(
      ['container', 'inspect', container],
      'workload inspection'
    );
    const root = singleInspection(output);
    const config = requiredRecord(root['Config'], 'container configuration');
    const host = requiredRecord(root['HostConfig'], 'host configuration');
    const state = requiredRecord(root['State'], 'container state');
    const network = requiredRecord(root['NetworkSettings'], 'network settings');
    const ports = requiredRecord(network['Ports'], 'port map');
    const labels = isRecord(config['Labels']) ? config['Labels'] : {};
    const image = stringValue(config, 'Image');
    const id = stringValue(root, 'Id');
    const networkName = stringValue(host, 'NetworkMode');
    if (!image || !id || !networkName) {
      throw new WorkloadRunnerError(
        'WorkloadFailed',
        'Docker inspection omitted required workload fields'
      );
    }
    const endpoint = loopbackEndpoint(ports);
    return {
      containerId: id,
      image,
      running: state['Running'] === true,
      user: stringValue(config, 'User'),
      readOnlyRootFilesystem: host['ReadonlyRootfs'] === true,
      droppedCapabilities: stringArray(host['CapDrop']),
      securityOptions: stringArray(host['SecurityOpt']),
      memoryBytes: numberValue(host, 'Memory'),
      nanoCpus: numberValue(host, 'NanoCpus'),
      pids: numberValue(host, 'PidsLimit'),
      networkName,
      ...(endpoint === undefined ? {} : { endpoint }),
      role: stringValue(labels, 'tenkacloud.simulator.role'),
      specHash: stringValue(labels, 'tenkacloud.simulator.spec'),
      workloadId: stringValue(labels, 'tenkacloud.simulator.workload'),
      worldId: stringValue(labels, 'tenkacloud.simulator.world'),
      targetId: stringValue(labels, 'tenkacloud.simulator.target'),
      resourceRef: stringValue(labels, 'tenkacloud.simulator.resource'),
      healthPath: stringValue(labels, 'tenkacloud.simulator.health') || '/',
      containerPort: Number(
        stringValue(labels, 'tenkacloud.simulator.port') || '0'
      ),
    };
  }

  async #worldInspections(
    worldId: string
  ): Promise<readonly WorkloadInspection[]> {
    requiredText(worldId, 'worldId');
    if (!WORLD_ID.test(worldId)) {
      throw new WorkloadRunnerError(
        'InvalidWorkload',
        'worldId must use the simulator world identifier format'
      );
    }
    const output = await this.#requiredDocker(
      [
        'container',
        'ls',
        '--all',
        `--filter=label=tenkacloud.simulator.world=${worldId}`,
        '--format={{.ID}}',
      ],
      'workload world listing'
    );
    const ids = output.split(/\s+/).filter(Boolean);
    const inspections: WorkloadInspection[] = [];
    for (const id of ids) {
      try {
        inspections.push(await this.inspect(id));
      } catch (error) {
        const stillExists = await this.#docker(['container', 'inspect', id]);
        if (stillExists.exitCode === 0) throw error;
      }
    }
    return inspections
      .filter((inspection) => inspection.worldId === worldId)
      .sort((left, right) => left.containerId.localeCompare(right.containerId));
  }

  async listWorld(worldId: string): Promise<readonly MaterializedWorkload[]> {
    const inspections = await this.#worldInspections(worldId);
    const workloads = inspections
      .filter((inspection) => inspection.role === 'workload')
      .sort((left, right) => left.workloadId.localeCompare(right.workloadId));
    return Promise.all(
      workloads.map(async (workload) => {
        if (!workload.running || !workload.workloadId) {
          throw new WorkloadRunnerError(
            'WorkloadFailed',
            'workload listing found an invalid container state'
          );
        }
        const proxies = inspections.filter(
          (candidate) =>
            candidate.role === 'proxy' &&
            candidate.workloadId === workload.workloadId
        );
        const proxy = proxies[0];
        if (
          workload.containerPort > 0 &&
          (proxies.length !== 1 ||
            !proxy?.running ||
            !proxy.endpoint ||
            proxy.specHash !== workload.specHash ||
            proxy.targetId !== workload.targetId ||
            proxy.resourceRef !== workload.resourceRef ||
            proxy.healthPath !== workload.healthPath ||
            proxy.containerPort !== workload.containerPort)
        ) {
          throw new WorkloadRunnerError(
            'WorkloadFailed',
            'workload listing found an invalid proxy state'
          );
        }
        return {
          containerId: workload.containerId,
          networkName: workload.networkName,
          worldId: workload.worldId,
          workloadId: workload.workloadId,
          targetId: workload.targetId,
          resourceRef: workload.resourceRef,
          image: workload.image,
          healthPath: workload.healthPath,
          ...(proxy?.endpoint === undefined
            ? {}
            : { endpoint: proxy.endpoint }),
          ...(proxy === undefined
            ? {}
            : { proxyContainerId: proxy.containerId }),
        };
      })
    );
  }

  async materialize(
    worldId: string,
    value: unknown
  ): Promise<readonly MaterializedWorkload[]> {
    const declarations = parseDeclarations(value);
    const specs = declarations.map(
      (declaration): WorkloadSpec => ({
        worldId,
        workloadId: declaration.id,
        image: declaration.image,
        containerPort: declaration.containerPort,
        targetId: declaration.targetId,
        resourceRef: declaration.resourceRef,
        ...(declaration.command === undefined
          ? {}
          : { command: declaration.command }),
        ...(declaration.healthPath === undefined
          ? {}
          : { healthPath: declaration.healthPath }),
      })
    );
    for (const spec of specs) this.#validate(spec);
    const existing = new Set(
      (await this.listWorld(worldId)).map((workload) => workload.containerId)
    );
    const created: RunningWorkload[] = [];
    try {
      for (const spec of specs) {
        const running = await this.start(spec);
        if (!existing.has(running.containerId)) created.push(running);
      }
      const requested = new Set(declarations.map((item) => item.id));
      return (await this.listWorld(worldId)).filter((workload) =>
        requested.has(workload.workloadId)
      );
    } catch (error) {
      let rollbackFailed = false;
      for (const workload of created.reverse()) {
        if (!(await this.stop(workload.containerId))) rollbackFailed = true;
      }
      if (rollbackFailed) {
        throw new WorkloadRunnerError(
          'WorkloadFailed',
          'workload materialization failed and rollback was incomplete'
        );
      }
      throw error;
    }
  }

  async probe(
    worldId: string,
    workloadId: string
  ): Promise<WorkloadProbeResult> {
    requiredText(workloadId, 'workloadId');
    const workload = (await this.listWorld(worldId)).find(
      (candidate) => candidate.workloadId === workloadId
    );
    if (!workload) {
      throw new WorkloadRunnerError(
        'WorkloadNotFound',
        'workload does not exist in the requested world'
      );
    }
    if (!workload.endpoint) {
      throw new WorkloadRunnerError(
        'WorkloadFailed',
        'workload does not expose a loopback endpoint'
      );
    }
    const endpoint = new URL(workload.endpoint);
    if (endpoint.protocol !== 'http:' || endpoint.hostname !== '127.0.0.1') {
      throw new WorkloadRunnerError(
        'WorkloadFailed',
        'workload endpoint is not simulator-owned loopback HTTP'
      );
    }
    let healthOrigin = endpoint.origin;
    if (this.policy.controlContainer !== undefined) {
      const inspection = await this.inspect(workload.containerId);
      if (
        !Number.isSafeInteger(inspection.containerPort) ||
        inspection.containerPort < 1024 ||
        inspection.containerPort > 65_535
      ) {
        throw new WorkloadRunnerError(
          'WorkloadFailed',
          'workload inspection contains an invalid internal health port'
        );
      }
      healthOrigin = `http://${identifier(
        'tc-sim-proxy',
        workload.worldId,
        workload.workloadId
      )}:${inspection.containerPort}`;
    }
    const response = await fetchOptional(
      `${healthOrigin}${workload.healthPath}`,
      {
        redirect: 'manual',
        signal: AbortSignal.timeout(1000),
      }
    );
    if (!response) {
      throw new WorkloadRunnerError(
        'WorkloadFailed',
        'workload health probe failed'
      );
    }
    const status = response.status;
    await response.body?.cancel();
    return {
      endpoint: workload.endpoint,
      healthPath: workload.healthPath,
      healthy: status >= 200 && status < 300,
      status,
    };
  }

  async logs(container: string): Promise<string> {
    return this.#requiredDocker(['logs', container], 'workload log read');
  }

  async stop(container: string): Promise<boolean> {
    let inspection: WorkloadInspection | undefined;
    try {
      inspection = await this.inspect(container);
    } catch {
      inspection = undefined;
    }
    if (!inspection) return false;
    if (
      inspection.role === 'workload' &&
      inspection.worldId &&
      inspection.workloadId
    ) {
      const proxyName = identifier(
        'tc-sim-proxy',
        inspection.worldId,
        inspection.workloadId
      );
      if (!(await this.#stopAndAwaitRemoval(proxyName))) return false;
    }
    return this.#stopAndAwaitRemoval(container);
  }

  async pruneWorld(worldId: string): Promise<void> {
    const inspections = await this.#worldInspections(worldId);
    const ordered = [...inspections].sort((left, right) => {
      if (left.role === right.role) {
        return left.containerId.localeCompare(right.containerId);
      }
      return left.role === 'proxy' ? -1 : 1;
    });
    for (const inspection of ordered) {
      if (!(await this.#stopAndAwaitRemoval(inspection.containerId))) {
        throw new WorkloadRunnerError(
          'WorkloadFailed',
          'workload world cleanup did not remove a container'
        );
      }
    }
    const networkName = this.#networkName(worldId);
    const network = await this.#docker(['network', 'inspect', networkName]);
    if (network.exitCode !== 0) return;
    await this.#disconnectControlContainer(networkName);
    const removed = await this.#docker(['network', 'rm', networkName]);
    if (removed.exitCode !== 0) {
      const stillExists = await this.#docker([
        'network',
        'inspect',
        networkName,
      ]);
      if (stillExists.exitCode === 0) {
        throw new WorkloadRunnerError(
          'WorkloadFailed',
          `workload network cleanup failed: ${safeDockerMessage(removed.stderr)}`
        );
      }
    }
  }

  async cleanup(worldId: string): Promise<void> {
    await this.pruneWorld(worldId);
  }
}
