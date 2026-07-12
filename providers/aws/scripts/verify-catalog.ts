import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import {
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import {
  compareInventory,
  serializeReport,
} from '../../../tools/catalog-scanner/src/manifest.ts';
import { collectCatalog } from '../../../tools/catalog-scanner/src/scanner.ts';
import {
  AWS_CATALOG_CAPABILITY_MANIFEST,
  unsupportedCatalogIdentities,
} from '../src/catalog-manifest';
import { compileCloudFormation } from '../src/cloudformation';
import { RUNTIME_ENDPOINT_RESOURCE, STACK_RESOURCE } from '../src/model';
import { AwsProvider } from '../src/provider';

function catalogArgument(args: readonly string[]): string | undefined {
  const index = args.indexOf('--catalog');
  return index === -1
    ? process.env['TENKACLOUD_CHALLENGE_ROOT']
    : args[index + 1];
}

const catalog = catalogArgument(process.argv.slice(2));
if (!catalog) {
  process.stderr.write(
    'Usage: bun scripts/verify-catalog.ts --catalog <TenkaCloudChallenge root>'
  );
  process.exitCode = 2;
} else {
  const inventory = await collectCatalog(catalog);
  const providerInventory = {
    ...inventory,
    requirements: inventory.requirements.filter(
      (requirement) =>
        requirement.provider === 'aws' && requirement.service !== 'runtime'
    ),
  };
  const report = compareInventory(
    providerInventory,
    AWS_CATALOG_CAPABILITY_MANIFEST
  );
  const unsupported = unsupportedCatalogIdentities(
    providerInventory.requirements.filter(
      (requirement) => requirement.classification === 'binding'
    )
  );
  const compiledTargets = [];
  for (const problem of inventory.problems) {
    const metadataPath = join(catalog, problem.metadataPath);
    const metadata: unknown = JSON.parse(await readFile(metadataPath, 'utf8'));
    for (const target of problem.targets) {
      if (target.provider !== 'aws' || target.engine !== 'cloudformation')
        continue;
      const templateBody = await readFile(
        join(dirname(metadataPath), target.entry),
        'utf8'
      );
      const plan = compileCloudFormation({
        target,
        targetId: target.targetId,
        problemId: problem.problemId,
        templateBody,
        artifacts: [],
        metadata,
      });
      const store = new SimulationStore(':memory:');
      const core = new SimulationCore(
        store,
        new ProviderRegistry([new AwsProvider()])
      );
      const deploymentId = `${problem.problemId}-${target.targetId}`;
      const world = core.createWorld(
        {
          tenantId: 'catalog-verification',
          eventId: problem.problemId,
          teamId: target.targetId,
          deploymentId,
          seed: 'catalog-verification',
        },
        `world-${deploymentId}`
      );
      const deployment = core.createDeployment(
        world.worldId,
        {
          deploymentId,
          problemId: problem.problemId,
          runtime: {
            id: target.targetId,
            provider: target.provider,
            engine: target.engine,
            entry: target.entry,
          },
          templateBody,
          metadata,
        },
        `deployment-${deploymentId}`
      );
      const sqliteResources = store.resources(world.worldId).length;
      const outputs = deployment.outputs[target.targetId] ?? {};
      store.close();
      compiledTargets.push({
        problemId: problem.problemId,
        targetId: target.targetId,
        resources: plan.resources.filter(
          (resource) =>
            resource.resourceType !== STACK_RESOURCE &&
            resource.resourceType !== RUNTIME_ENDPOINT_RESOURCE
        ).length,
        sqliteResources,
        outputs: Object.keys(outputs).sort(),
        resourceTypes: [
          ...new Set(
            plan.resources
              .filter(
                (resource) =>
                  resource.resourceType !== STACK_RESOURCE &&
                  resource.resourceType !== RUNTIME_ENDPOINT_RESOURCE
              )
              .map((resource) => resource.resourceType)
          ),
        ].sort(),
      });
    }
  }
  process.stdout.write(
    JSON.stringify(
      {
        summary: report.summary,
        compiledTargets,
        unsupportedIdentities: unsupported,
        coverageReport: JSON.parse(serializeReport(report)),
      },
      null,
      2
    ) + '\n'
  );
  process.exitCode = report.status === 'covered' ? 0 : 1;
}
