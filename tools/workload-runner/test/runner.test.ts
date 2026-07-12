import { afterEach, describe, expect, it } from 'bun:test';
import {
  DockerWorkloadRunner,
  type WorkloadDeclaration,
  WorkloadRunnerError,
  type WorkloadSpec,
} from '../src/index';

const IMAGE =
  'busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662';
const RUN_ID = crypto.randomUUID();
const WORLD_ID = `world-workload-contract-${RUN_ID}`;
const CONTROL_WORLD_ID = `world-control-container-contract-${RUN_ID}`;

const runner = new DockerWorkloadRunner({
  allowedImages: new Set([IMAGE]),
  proxyImage: IMAGE,
  maxMemoryBytes: 134_217_728,
  maxMilliCpu: 500,
  maxPids: 64,
});

const containers: string[] = [];
const controlContainers: string[] = [];

function docker(args: readonly string[]): {
  readonly exitCode: number;
  readonly stdout: string;
  readonly stderr: string;
} {
  const result = Bun.spawnSync(['docker', ...args]);
  return {
    exitCode: result.exitCode,
    stdout: result.stdout.toString().trim(),
    stderr: result.stderr.toString().trim(),
  };
}

function requiredDocker(args: readonly string[]): string {
  const result = docker(args);
  if (result.exitCode !== 0) {
    throw new Error(`Docker test command failed: ${result.stderr}`);
  }
  return result.stdout;
}

function startControlContainer(): {
  readonly controlName: string;
  readonly controlContainerId: string;
} {
  const controlName = `tenkacloud-simulator-${crypto.randomUUID()}`;
  const controlContainerId = requiredDocker([
    'run',
    '--detach',
    '--rm',
    `--name=${controlName}`,
    IMAGE,
    'sleep',
    '60',
  ]);
  controlContainers.push(controlContainerId);
  return { controlName, controlContainerId };
}

afterEach(async () => {
  for (const container of containers.splice(0)) {
    await runner.stop(container);
  }
  for (const container of controlContainers.splice(0)) {
    docker(['container', 'stop', '--time=1', container]);
  }
  await runner.cleanup(WORLD_ID);
  await runner.cleanup(CONTROL_WORLD_ID);
}, 60_000);

function workload(overrides: Partial<WorkloadSpec> = {}): WorkloadSpec {
  return {
    worldId: WORLD_ID,
    workloadId: 'http-service',
    image: IMAGE,
    command: [
      'sh',
      '-c',
      'sleep 0.6; printf "ready\\n" > /tmp/healthz; printf "started\\n"; exec httpd -f -p 8080 -h /tmp',
    ],
    environment: {
      SIMULATOR_MODE: 'local',
      SIMULATOR_REGION: 'local-1',
    },
    containerPort: 8080,
    healthPath: '/healthz',
    memoryBytes: 67_108_864,
    milliCpu: 250,
    pids: 32,
    ...overrides,
  };
}

function internalWorkload(overrides: Partial<WorkloadSpec> = {}): WorkloadSpec {
  return {
    worldId: CONTROL_WORLD_ID,
    workloadId: 'internal-service',
    image: IMAGE,
    command: ['sleep', '60'],
    memoryBytes: 67_108_864,
    milliCpu: 250,
    pids: 32,
    ...overrides,
  };
}

describe('Docker workload runner', () => {
  it.serial(
    'digest pin と allowlist を満たす workload を非特権・egress deny で起動する',
    async () => {
      expect(await runner.available()).toBe(true);
      const started = await runner.start(workload());
      containers.push(started.containerId);
      expect(started.endpoint).toStartWith('http://127.0.0.1:');
      expect(started.proxyContainerId).toBeString();

      const repeated = await runner.start(workload());
      expect(repeated).toEqual(started);
      const inspection = await runner.inspect(started.containerId);
      expect(inspection).toMatchObject({
        image: IMAGE,
        running: true,
        user: '65532:65532',
        readOnlyRootFilesystem: true,
        memoryBytes: 67_108_864,
        nanoCpus: 250_000_000,
        pids: 32,
        networkName: started.networkName,
      });
      expect(inspection.droppedCapabilities).toContain('ALL');
      expect(inspection.securityOptions).toContain('no-new-privileges:true');
      expect(await runner.logs(started.containerId)).toContain('started');

      const network = Bun.spawnSync([
        'docker',
        'network',
        'inspect',
        started.networkName,
        '--format',
        '{{.Internal}}',
      ]);
      expect(network.exitCode).toBe(0);
      expect(network.stdout.toString().trim()).toBe('true');

      const response = await fetch(`${started.endpoint}/healthz`);
      expect(response.status).toBe(200);
      expect(await runner.probe(WORLD_ID, 'http-service')).toEqual({
        endpoint: started.endpoint ?? '',
        healthPath: '/healthz',
        healthy: true,
        status: 200,
      });
      expect(await runner.listWorld(WORLD_ID)).toEqual([
        expect.objectContaining({
          containerId: started.containerId,
          endpoint: started.endpoint,
          healthPath: '/healthz',
          image: IMAGE,
          workloadId: 'http-service',
          worldId: WORLD_ID,
        }),
      ]);
      expect(await runner.stop(started.containerId)).toBe(true);
      expect(await runner.stop(started.containerId)).toBe(false);
      containers.splice(0);
    },
    60_000
  );

  it.serial(
    'control container名をworld networkへ冪等接続しprune前に切断する',
    async () => {
      const { controlName, controlContainerId } = startControlContainer();
      const controlledByName = new DockerWorkloadRunner({
        ...runner.policy,
        controlContainer: controlName,
      });
      const spec = internalWorkload({
        workloadId: 'internal-health-service',
        command: [
          'sh',
          '-c',
          'printf "ready\\n" > /tmp/healthz; exec httpd -f -p 8080 -h /tmp',
        ],
      });

      const started = await controlledByName.start(spec);
      containers.push(started.containerId);
      expect(started.endpoint).toBeUndefined();
      expect(started.proxyContainerId).toBeUndefined();
      expect(await controlledByName.start(spec)).toEqual(started);

      const controlledById = new DockerWorkloadRunner({
        ...runner.policy,
        controlContainer: controlContainerId,
      });
      expect(await controlledById.start(spec)).toEqual(started);

      const membership = JSON.parse(
        requiredDocker([
          'network',
          'inspect',
          started.networkName,
          '--format={{json .Containers}}',
        ])
      );
      expect(membership).toHaveProperty(controlContainerId);
      expect(membership).toHaveProperty(started.containerId);

      const workloadName = requiredDocker([
        'container',
        'inspect',
        started.containerId,
        '--format={{.Name}}',
      ]).replace(/^\//, '');
      let health = docker([
        'container',
        'exec',
        controlContainerId,
        'wget',
        '-qO-',
        `http://${workloadName}:8080/healthz`,
      ]);
      for (
        let attempt = 0;
        health.exitCode !== 0 && attempt < 20;
        attempt += 1
      ) {
        await Bun.sleep(50);
        health = docker([
          'container',
          'exec',
          controlContainerId,
          'wget',
          '-qO-',
          `http://${workloadName}:8080/healthz`,
        ]);
      }
      expect(health.exitCode).toBe(0);
      expect(health.stdout).toBe('ready');

      await controlledByName.pruneWorld(CONTROL_WORLD_ID);
      const startedIndex = containers.indexOf(started.containerId);
      if (startedIndex >= 0) containers.splice(startedIndex, 1);
      expect(
        JSON.parse(
          requiredDocker([
            'container',
            'inspect',
            controlContainerId,
            '--format={{json .NetworkSettings.Networks}}',
          ])
        )
      ).not.toHaveProperty(started.networkName);
      expect(
        requiredDocker([
          'container',
          'inspect',
          controlContainerId,
          '--format={{.State.Running}}',
        ])
      ).toBe('true');
      expect(
        docker(['network', 'inspect', started.networkName]).exitCode
      ).not.toBe(0);
      await controlledByName.pruneWorld(CONTROL_WORLD_ID);
    },
    60_000
  );

  it.serial(
    'control modeのhealthとprobeをinternal proxy経路へ固定する',
    async () => {
      const spec = workload({
        worldId: CONTROL_WORLD_ID,
        workloadId: 'proxy-health-service',
      });
      const started = await runner.start(spec);
      containers.push(started.containerId);
      expect(started.endpoint).toStartWith('http://127.0.0.1:');
      const { controlName, controlContainerId } = startControlContainer();
      const controlled = new DockerWorkloadRunner({
        ...runner.policy,
        controlContainer: controlName,
      });

      await expect(controlled.start(spec)).rejects.toThrow(
        'workload proxy did not become reachable'
      );
      const proxyName = requiredDocker([
        'container',
        'inspect',
        started.proxyContainerId ?? '',
        '--format={{.Name}}',
      ]).replace(/^\//, '');
      let internal = docker([
        'container',
        'exec',
        controlContainerId,
        'wget',
        '-qO-',
        `http://${proxyName}:8080/healthz`,
      ]);
      for (
        let attempt = 0;
        internal.exitCode !== 0 && attempt < 20;
        attempt += 1
      ) {
        await Bun.sleep(50);
        internal = docker([
          'container',
          'exec',
          controlContainerId,
          'wget',
          '-qO-',
          `http://${proxyName}:8080/healthz`,
        ]);
      }
      expect(internal).toMatchObject({ exitCode: 0, stdout: 'ready' });
      expect(
        await runner.probe(CONTROL_WORLD_ID, spec.workloadId)
      ).toMatchObject({
        endpoint: started.endpoint,
        healthy: true,
        status: 200,
      });
      await expect(
        controlled.probe(CONTROL_WORLD_ID, spec.workloadId)
      ).rejects.toMatchObject({ code: 'WorkloadFailed' });

      await controlled.pruneWorld(CONTROL_WORLD_ID);
      expect(
        docker(['network', 'inspect', started.networkName]).exitCode
      ).not.toBe(0);
    },
    60_000
  );

  it.serial(
    'control container selectorをsafe nameまたは完全IDに限定する',
    async () => {
      for (const controlContainer of [
        '',
        '-leading-option',
        'name/with/slash',
        'name with space',
        'a'.repeat(12),
        'x'.repeat(129),
      ]) {
        expect(
          () =>
            new DockerWorkloadRunner({
              ...runner.policy,
              controlContainer,
            })
        ).toThrow('non-empty');
      }

      const missing = new DockerWorkloadRunner({
        ...runner.policy,
        controlContainer: 'tenkacloud-simulator-missing',
      });
      await expect(
        missing.start(
          internalWorkload({
            workloadId: 'missing-control-container',
          })
        )
      ).rejects.toMatchObject({ code: 'WorkloadFailed' });
      await missing.pruneWorld(CONTROL_WORLD_ID);
    },
    60_000
  );

  it.serial(
    'tag、allowlist 外 image、secret 環境変数、過剰 quota を loud に拒否する',
    async () => {
      const cases: WorkloadSpec[] = [
        workload({ image: 'busybox:1.36' }),
        workload({
          image:
            'alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
        }),
        workload({ environment: { ACCESS_TOKEN: 'must-not-enter-runner' } }),
        workload({ environment: { invalid: 'value' } }),
        workload({ environment: { VALUE: 'x'.repeat(4097) } }),
        workload({ environment: { VALUE: 'contains\u0000null' } }),
        workload({ containerPort: 80 }),
        workload({ containerPort: 70_000 }),
        workload({ healthPath: 'healthz' }),
        workload({ healthPath: `/${'x'.repeat(256)}` }),
        workload({ command: ['printf', 'contains\u0000null'] }),
        workload({ memoryBytes: 134_217_729 }),
        workload({ milliCpu: 0 }),
        workload({ pids: 1.5 }),
        workload({ worldId: '' }),
        workload({ workloadId: 'x'.repeat(257) }),
      ];
      for (const spec of cases) {
        await expect(runner.start(spec)).rejects.toBeInstanceOf(
          WorkloadRunnerError
        );
      }
      expect(
        () =>
          new DockerWorkloadRunner({
            allowedImages: new Set(),
            proxyImage: 'busybox:latest',
            maxMemoryBytes: 0,
            maxMilliCpu: 0,
            maxPids: 0,
          })
      ).toThrow('non-empty');
      await expect(runner.pruneWorld('')).rejects.toMatchObject({
        code: 'InvalidWorkload',
      });
      await expect(runner.listWorld('')).rejects.toMatchObject({
        code: 'InvalidWorkload',
      });
      await expect(runner.probe(WORLD_ID, 'missing')).rejects.toMatchObject({
        code: 'WorkloadNotFound',
      });
    }
  );

  it.serial(
    'strict overlay の workload を materialize して label から実 endpoint を復元する',
    async () => {
      const declaration: WorkloadDeclaration = {
        id: 'catalog-service',
        targetId: 'default',
        resourceRef: 'CatalogService',
        image: IMAGE,
        command: workload().command ?? [],
        containerPort: 8080,
        healthPath: '/healthz',
      };

      const worker: WorkloadDeclaration = {
        ...declaration,
        id: 'worker-service',
        resourceRef: 'WorkerService',
      };
      const materialized = await runner.materialize(WORLD_ID, [
        worker,
        declaration,
      ]);
      containers.push(...materialized.map((item) => item.containerId));
      expect(materialized).toEqual([
        expect.objectContaining({
          targetId: 'default',
          resourceRef: 'CatalogService',
          workloadId: 'catalog-service',
          healthPath: '/healthz',
          image: IMAGE,
        }),
        expect.objectContaining({
          resourceRef: 'WorkerService',
          workloadId: 'worker-service',
        }),
      ]);
      expect(
        await fetch(`${materialized[0]?.endpoint}/healthz`)
      ).toHaveProperty('status', 200);
      expect(await runner.materialize(WORLD_ID, [worker, declaration])).toEqual(
        materialized
      );
      expect(await runner.listWorld(WORLD_ID)).toEqual(materialized);

      await runner.pruneWorld(WORLD_ID);
      containers.splice(0);
      expect(await runner.listWorld(WORLD_ID)).toEqual([]);
      const network = Bun.spawnSync([
        'docker',
        'network',
        'inspect',
        materialized[0]?.networkName ?? '',
      ]);
      expect(network.exitCode).not.toBe(0);
      await runner.pruneWorld(WORLD_ID);
    },
    60_000
  );

  it.serial(
    'overlay の unknown field、secret 相当 field、重複 ID、境界違反を起動前に拒否する',
    async () => {
      const validWorkload = {
        id: 'catalog-service',
        targetId: 'default',
        resourceRef: 'CatalogService',
        image: IMAGE,
        command: ['httpd', '-f', '-p', '8080'],
        containerPort: 8080,
        healthPath: '/healthz',
      };
      const invalid: unknown[] = [
        null,
        {},
        Array.from({ length: 33 }, (_, index) => ({
          ...validWorkload,
          id: `service-${index}`,
        })),
        [validWorkload, validWorkload],
        [{ ...validWorkload, environment: { TOKEN: 'hidden' } }],
        [{ ...validWorkload, id: 'Bad' }],
        [{ ...validWorkload, targetId: '../target' }],
        [{ ...validWorkload, resourceRef: '\ninvalid' }],
        [{ ...validWorkload, image: 'busybox:latest' }],
        [{ ...validWorkload, command: [] }],
        [{ ...validWorkload, command: ['x'.repeat(513)] }],
        [{ ...validWorkload, command: ['contains\u0000null'] }],
        [{ ...validWorkload, containerPort: 80 }],
        [{ ...validWorkload, healthPath: '//example.test/path' }],
      ];
      for (const declarations of invalid) {
        await expect(
          runner.materialize(WORLD_ID, declarations)
        ).rejects.toMatchObject({ code: 'InvalidWorkload' });
      }
      expect(await runner.listWorld(WORLD_ID)).toEqual([]);
    }
  );

  it.serial(
    '複数 workload の一部が起動失敗したとき今回作成した container を rollback する',
    async () => {
      const declarations: readonly WorkloadDeclaration[] = [
        {
          id: 'a-running-service',
          targetId: 'default',
          resourceRef: 'RunningService',
          image: IMAGE,
          command: workload().command ?? [],
          containerPort: 8080,
          healthPath: '/healthz',
        },
        {
          id: 'z-failing-service',
          targetId: 'default',
          resourceRef: 'FailingService',
          image: IMAGE,
          command: ['this-command-does-not-exist'],
          containerPort: 8080,
        },
      ];
      await expect(
        runner.materialize(WORLD_ID, declarations)
      ).rejects.toMatchObject({ code: 'WorkloadFailed' });
      expect(await runner.listWorld(WORLD_ID)).toEqual([]);
    },
    60_000
  );

  it.serial(
    '起動後に data plane が停止した workload の health probe を typed failure にする',
    async () => {
      const started = await runner.start(
        workload({
          workloadId: 'stopped-data-plane',
          command: [
            'sh',
            '-c',
            'printf "ready\\n" > /tmp/healthz; httpd -p 8080 -h /tmp; sleep 2; killall httpd; exec sleep 10',
          ],
        })
      );
      containers.push(started.containerId);
      await Bun.sleep(2200);
      await expect(
        runner.probe(WORLD_ID, 'stopped-data-plane')
      ).rejects.toMatchObject({ code: 'WorkloadFailed' });
    },
    60_000
  );

  it.serial(
    'Docker 実行不能、存在しない container、起動失敗を typed error にする',
    async () => {
      const unavailable = new DockerWorkloadRunner(
        {
          allowedImages: new Set([IMAGE]),
          proxyImage: IMAGE,
          maxMemoryBytes: 1024,
          maxMilliCpu: 1,
          maxPids: 1,
        },
        '/path/that/does/not/exist/docker'
      );
      await expect(unavailable.available()).rejects.toMatchObject({
        code: 'RunnerUnavailable',
      });
      const hanging = new DockerWorkloadRunner(
        {
          allowedImages: new Set([IMAGE]),
          proxyImage: IMAGE,
          maxMemoryBytes: 1024,
          maxMilliCpu: 1,
          maxPids: 1,
        },
        new URL('./fixtures/hanging-docker.sh', import.meta.url).pathname,
        1
      );
      await expect(hanging.available()).rejects.toMatchObject({
        code: 'RunnerUnavailable',
      });
      expect(
        () =>
          new DockerWorkloadRunner(
            {
              allowedImages: new Set([IMAGE]),
              proxyImage: IMAGE,
              maxMemoryBytes: 1024,
              maxMilliCpu: 1,
              maxPids: 1,
            },
            'docker',
            0
          )
      ).toThrow('non-empty');
      await expect(runner.inspect('not-a-container')).rejects.toMatchObject({
        code: 'WorkloadFailed',
      });
      const longDiagnostic = await runner
        .inspect('x'.repeat(600))
        .catch((error: unknown) => error);
      expect(longDiagnostic).toBeInstanceOf(WorkloadRunnerError);
      expect(String(longDiagnostic).length).toBeLessThanOrEqual(560);
      const failed = await runner
        .start({
          worldId: WORLD_ID,
          workloadId: 'failing-command',
          image: IMAGE,
          command: ['this-command-does-not-exist'],
        })
        .catch((error: unknown) => error);
      expect(failed).toBeInstanceOf(WorkloadRunnerError);
    }
  );
});
