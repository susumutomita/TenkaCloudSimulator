import type {
  JsonValue,
  SimulatorDiagnostic,
  SimulatorEvent,
  SimulatorResourceRecord,
} from '@tenkacloud/simulator-contracts';
import {
  type ConsoleLoadState,
  diagnostics,
  displayValue,
  groupResources,
  propertyCategories,
} from './model';

export interface WorldConsoleViewProps {
  readonly state: ConsoleLoadState;
  readonly onRefresh: () => void;
}

function BrandMark(): React.JSX.Element {
  return (
    <span className="brand-mark" aria-hidden="true">
      T
    </span>
  );
}

function ShellHeader(): React.JSX.Element {
  return (
    <header className="shell-header">
      <a className="brand" href="/" aria-label="TenkaCloud home">
        <BrandMark />
        <span>TenkaCloud</span>
        <span className="brand-product">Simulator</span>
      </a>
      <div className="protocol-badge">
        <span className="live-dot" aria-hidden="true" />
        Protocol 2026-07-11
      </div>
    </header>
  );
}

function LoadingState({
  worldId,
}: {
  readonly worldId: string;
}): React.JSX.Element {
  return (
    <main className="state-page" aria-busy="true" aria-live="polite">
      <div className="loader" aria-hidden="true" />
      <p className="eyebrow">World {worldId}</p>
      <h1>Reading the event-sourced world</h1>
      <p>Loading resources, deployment output, and the replay cursor.</p>
    </main>
  );
}

function ErrorState({
  worldId,
  message,
  onRefresh,
}: {
  readonly worldId: string;
  readonly message: string;
  readonly onRefresh: () => void;
}): React.JSX.Element {
  return (
    <main className="state-page" role="alert">
      <span className="state-icon" aria-hidden="true">
        !
      </span>
      <p className="eyebrow">World {worldId}</p>
      <h1>World unavailable</h1>
      <p>{message}</p>
      <button className="primary-button" type="button" onClick={onRefresh}>
        Try again
      </button>
    </main>
  );
}

function Metric({
  label,
  value,
}: {
  readonly label: string;
  readonly value: string | number;
}): React.JSX.Element {
  return (
    <div className="metric">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function StatusBadge({
  status,
}: {
  readonly status: string;
}): React.JSX.Element {
  return <span className={`status status-${status}`}>{status}</span>;
}

function JsonEntry({
  name,
  value,
}: {
  readonly name: string;
  readonly value: JsonValue;
}): React.JSX.Element {
  const rendered = displayValue(value);
  return (
    <div className="property-row">
      <dt>{name}</dt>
      <dd className={rendered.includes('\n') ? 'code-value' : undefined}>
        {rendered}
      </dd>
    </div>
  );
}

function ResourceCard({
  resource,
}: {
  readonly resource: SimulatorResourceRecord;
}): React.JSX.Element {
  return (
    <article className="resource-card">
      <div className="resource-heading">
        <div>
          <p className="resource-type">{resource.resourceType}</p>
          <h4>{resource.resourceId}</h4>
        </div>
        <StatusBadge status={resource.status} />
      </div>
      <div className="resource-meta">
        <span>Deployment</span>
        <code>{resource.deploymentId}</code>
      </div>
      {propertyCategories(resource).map((category) => (
        <details key={category.label} open={category.label !== 'Properties'}>
          <summary>{category.label}</summary>
          <dl className="properties">
            {category.entries.map(([name, value]) => (
              <JsonEntry key={name} name={name} value={value} />
            ))}
          </dl>
        </details>
      ))}
    </article>
  );
}

function ResourceGraph({
  resources,
}: {
  readonly resources: readonly SimulatorResourceRecord[];
}): React.JSX.Element {
  const groups = groupResources({ resources });
  if (groups.length === 0) {
    return <p className="empty-state">No resources have been projected yet.</p>;
  }
  return (
    <div className="provider-grid">
      {groups.map((group) => (
        <section className="provider-lane" key={group.provider}>
          <header>
            <span className="provider-glyph" aria-hidden="true">
              {group.provider.slice(0, 2).toUpperCase()}
            </span>
            <div>
              <h3>{group.provider}</h3>
              <p>{group.resources.length} projected resources</p>
            </div>
          </header>
          <div className="resource-list">
            {group.resources.map((resource) => (
              <ResourceCard
                key={`${resource.resourceType}:${resource.resourceId}`}
                resource={resource}
              />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function EventItem({
  event,
}: {
  readonly event: SimulatorEvent;
}): React.JSX.Element {
  return (
    <li className="event-item">
      <span className="event-sequence">{event.sequence}</span>
      <div>
        <div className="event-heading">
          <strong>{event.type}</strong>
          <time dateTime={event.virtualTimestamp}>
            {event.virtualTimestamp}
          </time>
        </div>
        <p className="event-command">
          {event.command.operation} · <code>{event.command.id}</code>
        </p>
        <details>
          <summary>Event payload</summary>
          <pre>{JSON.stringify(event.payload, null, 2)}</pre>
        </details>
      </div>
    </li>
  );
}

function EventTimeline({
  events,
}: {
  readonly events: readonly SimulatorEvent[];
}): React.JSX.Element {
  if (events.length === 0) {
    return <p className="empty-state">No events exist after this cursor.</p>;
  }
  return (
    <ol className="timeline">
      {events.toReversed().map((event) => (
        <EventItem key={event.sequence} event={event} />
      ))}
    </ol>
  );
}

function OutputList({
  outputs,
}: {
  readonly outputs: Readonly<Record<string, string>>;
}): React.JSX.Element {
  const entries = Object.entries(outputs);
  if (entries.length === 0) {
    return <p className="empty-state compact">No deployment outputs.</p>;
  }
  return (
    <dl className="output-list">
      {entries.map(([key, value]) => (
        <div key={key}>
          <dt>{key}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function DiagnosticItem({
  diagnostic,
}: {
  readonly diagnostic: SimulatorDiagnostic;
}): React.JSX.Element {
  return (
    <li>
      <strong>{diagnostic.code}</strong>
      <p>{diagnostic.message}</p>
      {diagnostic.source ? (
        <code>
          {diagnostic.source.file}
          {diagnostic.source.line ? `:${diagnostic.source.line}` : ''}
        </code>
      ) : null}
    </li>
  );
}

function Diagnostics({
  entries,
}: {
  readonly entries: readonly SimulatorDiagnostic[];
}): React.JSX.Element {
  if (entries.length === 0) {
    return <p className="empty-state compact">No deployment diagnostics.</p>;
  }
  return (
    <ul className="diagnostic-list">
      {entries.map((diagnostic) => (
        <DiagnosticItem
          key={`${diagnostic.code}:${diagnostic.provider}:${diagnostic.service}:${diagnostic.resourceType}:${diagnostic.operation}:${diagnostic.source?.file}:${diagnostic.source?.line}`}
          diagnostic={diagnostic}
        />
      ))}
    </ul>
  );
}

function ReadyConsole({
  state,
  onRefresh,
}: {
  readonly state: Extract<ConsoleLoadState, { readonly kind: 'ready' }>;
  readonly onRefresh: () => void;
}): React.JSX.Element {
  const { data } = state;
  const providers = groupResources(data.resources);
  const deploymentDiagnostics = diagnostics(data.deployment);
  return (
    <main className="console-main">
      <section className="hero" aria-labelledby="world-title">
        <div>
          <p className="eyebrow">Deterministic world</p>
          <h1 id="world-title">{data.worldId}</h1>
          <p className="hero-copy">
            One source of truth for every provider, command, and projected
            resource.
          </p>
        </div>
        <button className="secondary-button" type="button" onClick={onRefresh}>
          Refresh projection
        </button>
      </section>

      <section className="metrics" aria-label="World summary">
        <Metric label="Providers" value={providers.length} />
        <Metric label="Resources" value={data.resources.resources.length} />
        <Metric label="Events" value={data.events.length} />
        <Metric
          label="Deployment"
          value={data.deployment?.status ?? 'not selected'}
        />
      </section>

      <div className="content-grid">
        <section
          className="panel resources-panel"
          aria-labelledby="resources-title"
        >
          <div className="section-heading">
            <div>
              <p className="eyebrow">Resource graph</p>
              <h2 id="resources-title">Provider projections</h2>
            </div>
            <span className="cursor-badge">cursor {data.cursor}</span>
          </div>
          <ResourceGraph resources={data.resources.resources} />
        </section>

        <aside className="side-column">
          <section className="panel" aria-labelledby="outputs-title">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Deployment</p>
                <h2 id="outputs-title">Outputs</h2>
              </div>
              {data.deployment ? (
                <StatusBadge status={data.deployment.status} />
              ) : null}
            </div>
            <OutputList outputs={data.deployment?.outputs ?? {}} />
          </section>
          <section className="panel" aria-labelledby="diagnostics-title">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Preflight & runtime</p>
                <h2 id="diagnostics-title">Diagnostics</h2>
              </div>
              <span className="count-badge">
                {deploymentDiagnostics.length}
              </span>
            </div>
            <Diagnostics entries={deploymentDiagnostics} />
          </section>
        </aside>
      </div>

      <section className="panel events-panel" aria-labelledby="events-title">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Replayable audit trail</p>
            <h2 id="events-title">Event timeline</h2>
          </div>
          <span className="stream-badge">
            <span className="live-dot" aria-hidden="true" /> SSE replay
          </span>
        </div>
        <EventTimeline events={data.events} />
      </section>
    </main>
  );
}

export function WorldConsoleView({
  state,
  onRefresh,
}: WorldConsoleViewProps): React.JSX.Element {
  return (
    <div className="app-shell">
      <ShellHeader />
      {state.kind === 'loading' ? (
        <LoadingState worldId={state.worldId} />
      ) : null}
      {state.kind === 'error' ? (
        <ErrorState
          worldId={state.worldId}
          message={state.message}
          onRefresh={onRefresh}
        />
      ) : null}
      {state.kind === 'ready' ? (
        <ReadyConsole state={state} onRefresh={onRefresh} />
      ) : null}
      <footer className="shell-footer">
        <span>TenkaCloud Simulator</span>
        <span>Event-sourced · provider-neutral · deterministic</span>
      </footer>
    </div>
  );
}
