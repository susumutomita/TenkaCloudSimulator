import type {
  JsonValue,
  SimulatorDeploymentResponse,
  SimulatorDiagnostic,
  SimulatorEvent,
  SimulatorResourceProjection,
  SimulatorResourceRecord,
} from '@tenkacloud/simulator-contracts';

export interface ConsoleWorldData {
  readonly worldId: string;
  readonly deployment?: SimulatorDeploymentResponse;
  readonly resources: SimulatorResourceProjection;
  readonly events: readonly SimulatorEvent[];
  readonly cursor: number;
}

export type ConsoleLoadState =
  | { readonly kind: 'loading'; readonly worldId: string }
  | {
      readonly kind: 'error';
      readonly worldId: string;
      readonly message: string;
    }
  | { readonly kind: 'ready'; readonly data: ConsoleWorldData };

export interface ProviderResourceGroup {
  readonly provider: string;
  readonly resources: readonly SimulatorResourceRecord[];
}

export interface PropertyCategory {
  readonly label: 'Policy' | 'Reachability' | 'Properties';
  readonly entries: readonly (readonly [string, JsonValue])[];
}

const POLICY_PATTERN = /action|member|permission|policy|principal|role/i;
const REACHABILITY_PATTERN =
  /cidr|egress|endpoint|firewall|host|ingress|listener|network|port|public|route|security|subnet|uri|url/i;

export function groupResources(
  projection: SimulatorResourceProjection
): readonly ProviderResourceGroup[] {
  const providers = new Map<string, SimulatorResourceRecord[]>();
  for (const resource of projection.resources) {
    const entries = providers.get(resource.provider) ?? [];
    entries.push(resource);
    providers.set(resource.provider, entries);
  }
  return [...providers.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([provider, resources]) => ({
      provider,
      resources: resources.toSorted((left, right) =>
        left.resourceId.localeCompare(right.resourceId)
      ),
    }));
}

export function propertyCategories(
  resource: SimulatorResourceRecord
): readonly PropertyCategory[] {
  const policy: (readonly [string, JsonValue])[] = [];
  const reachability: (readonly [string, JsonValue])[] = [];
  const properties: (readonly [string, JsonValue])[] = [];
  for (const entry of Object.entries(resource.properties)) {
    if (POLICY_PATTERN.test(entry[0])) policy.push(entry);
    else if (REACHABILITY_PATTERN.test(entry[0])) reachability.push(entry);
    else properties.push(entry);
  }
  const categories: PropertyCategory[] = [
    { label: 'Policy', entries: policy },
    { label: 'Reachability', entries: reachability },
    { label: 'Properties', entries: properties },
  ];
  return categories.filter((category) => category.entries.length > 0);
}

export function diagnostics(
  deployment: SimulatorDeploymentResponse | undefined
): readonly SimulatorDiagnostic[] {
  if (!deployment) return [];
  const combined = [...(deployment.diagnostics ?? [])];
  for (const target of deployment.targets ?? []) {
    combined.push(...(target.diagnostics ?? []));
  }
  return combined;
}

export function mergeEvents(
  current: readonly SimulatorEvent[],
  incoming: readonly SimulatorEvent[]
): readonly SimulatorEvent[] {
  const bySequence = new Map(
    [...current, ...incoming].map((event) => [event.sequence, event])
  );
  return [...bySequence.values()].toSorted(
    (left, right) => left.sequence - right.sequence
  );
}

export function displayValue(value: JsonValue): string {
  if (typeof value === 'string') return value;
  return JSON.stringify(value, null, 2);
}
