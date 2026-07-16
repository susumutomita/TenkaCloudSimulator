import Alert from '@cloudscape-design/components/alert';
import AppLayout from '@cloudscape-design/components/app-layout';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Cards, { type CardsProps } from '@cloudscape-design/components/cards';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import ExpandableSection from '@cloudscape-design/components/expandable-section';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Grid from '@cloudscape-design/components/grid';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import StatusIndicator, {
  type StatusIndicatorProps,
} from '@cloudscape-design/components/status-indicator';
import Table, { type TableProps } from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';
import TopNavigation from '@cloudscape-design/components/top-navigation';
import type {
  JsonValue,
  SimulatorDiagnostic,
  SimulatorEvent,
  SimulatorResourceRecord,
} from '@tenkacloud/simulator-contracts';
import { useActionState, useRef, useState } from 'react';
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
  readonly onOperation: (formData: FormData) => Promise<void>;
}

export type ConsoleOperationActionState =
  | { readonly kind: 'idle' }
  | { readonly kind: 'success'; readonly message: string }
  | { readonly kind: 'error'; readonly message: string };

const INITIAL_OPERATION_STATE: ConsoleOperationActionState = { kind: 'idle' };

export async function runConsoleOperationAction(
  onOperation: (formData: FormData) => Promise<void>,
  _current: ConsoleOperationActionState,
  formData: FormData
): Promise<ConsoleOperationActionState> {
  try {
    await onOperation(formData);
    return {
      kind: 'success',
      message: 'Command accepted. Waiting for the shared projection.',
    };
  } catch (error) {
    return {
      kind: 'error',
      message:
        error instanceof Error
          ? error.message
          : 'The provider operation failed.',
    };
  }
}

const STATUS_INDICATOR_TYPES: Readonly<
  Record<string, StatusIndicatorProps.Type>
> = {
  accepted: 'pending',
  deleted: 'stopped',
  deleting: 'in-progress',
  deploying: 'in-progress',
  failed: 'error',
  pending: 'pending',
  ready: 'success',
  running: 'success',
};

export function statusIndicatorType(status: string): StatusIndicatorProps.Type {
  return STATUS_INDICATOR_TYPES[status] ?? 'pending';
}

function ConsoleStatus({
  status,
}: {
  readonly status: string;
}): React.JSX.Element {
  return (
    <StatusIndicator type={statusIndicatorType(status)}>
      {status}
    </StatusIndicator>
  );
}

export function ConsoleOperationResult({
  state,
}: {
  readonly state: ConsoleOperationActionState;
}): React.JSX.Element | null {
  if (state.kind === 'idle') return null;
  return (
    <div role={state.kind === 'error' ? 'alert' : 'status'}>
      <Alert type={state.kind === 'error' ? 'error' : 'success'}>
        {state.message}
      </Alert>
    </div>
  );
}

function LoadingState({
  worldId,
}: {
  readonly worldId: string;
}): React.JSX.Element {
  return (
    <div aria-busy="true" aria-live="polite">
      <Container>
        <Box padding="xxl" textAlign="center">
          <SpaceBetween alignItems="center" size="s">
            <Spinner size="large" />
            <Box variant="strong">World {worldId}</Box>
            <Box variant="h1">Reading the event-sourced world</Box>
            <Box color="text-body-secondary">
              Loading resources, deployment output, and the replay cursor.
            </Box>
          </SpaceBetween>
        </Box>
      </Container>
    </div>
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
    <div role="alert">
      <Alert
        action={<Button onClick={onRefresh}>Try again</Button>}
        header="World unavailable"
        type="error"
      >
        <SpaceBetween size="xxs">
          <Box variant="strong">World {worldId}</Box>
          <Box>{message}</Box>
        </SpaceBetween>
      </Alert>
    </div>
  );
}

function PropertyValue({
  value,
}: {
  readonly value: JsonValue;
}): React.JSX.Element {
  const rendered = displayValue(value);
  const variant = rendered.includes('\n') ? 'pre' : 'span';
  return <Box variant={variant}>{rendered}</Box>;
}

function ResourceCategories({
  resource,
}: {
  readonly resource: SimulatorResourceRecord;
}): React.JSX.Element {
  return (
    <SpaceBetween size="xs">
      {propertyCategories(resource).map((category) => (
        <ExpandableSection
          defaultExpanded={category.label !== 'Properties'}
          headerText={category.label}
          key={category.label}
        >
          <KeyValuePairs
            columns={2}
            items={category.entries.map(([name, value]) => ({
              label: name,
              value: <PropertyValue value={value} />,
            }))}
          />
        </ExpandableSection>
      ))}
    </SpaceBetween>
  );
}

function resourceKey(resource: SimulatorResourceRecord): string {
  return `${resource.deploymentId}:${resource.targetId}:${resource.provider}:${resource.resourceType}:${resource.resourceId}`;
}

const RESOURCE_CARD_DEFINITION: CardsProps.CardDefinition<SimulatorResourceRecord> =
  {
    header: (resource) => (
      <SpaceBetween direction="horizontal" size="xs">
        <Box variant="h4">{resource.resourceId}</Box>
        <ConsoleStatus status={resource.status} />
      </SpaceBetween>
    ),
    sections: [
      {
        id: 'type',
        header: 'Type',
        content: (resource) => (
          <Box variant="code">{resource.resourceType}</Box>
        ),
      },
      {
        id: 'placement',
        content: (resource) => (
          <KeyValuePairs
            columns={2}
            items={[
              {
                label: 'Target',
                value: <Box variant="code">{resource.targetId}</Box>,
              },
              {
                label: 'Deployment',
                value: <Box variant="code">{resource.deploymentId}</Box>,
              },
            ]}
          />
        ),
      },
      {
        id: 'categories',
        content: (resource) => <ResourceCategories resource={resource} />,
      },
    ],
  };

function ResourceGraph({
  resources,
}: {
  readonly resources: readonly SimulatorResourceRecord[];
}): React.JSX.Element {
  const groups = groupResources({ resources });
  if (groups.length === 0) {
    return (
      <Container>
        <Cards
          cardDefinition={RESOURCE_CARD_DEFINITION}
          empty={
            <Box color="text-body-secondary">
              No resources have been projected yet.
            </Box>
          }
          items={[]}
          trackBy={resourceKey}
        />
      </Container>
    );
  }
  return (
    <SpaceBetween size="m">
      {groups.map((group) => (
        <Container
          header={
            <Header
              counter={`(${group.resources.length})`}
              description={`${group.resources.length} projected resources`}
              variant="h3"
            >
              {group.provider}
            </Header>
          }
          key={group.provider}
        >
          <Cards
            cardDefinition={RESOURCE_CARD_DEFINITION}
            cardsPerRow={[{ cards: 1 }]}
            items={[...group.resources]}
            trackBy={resourceKey}
          />
        </Container>
      ))}
    </SpaceBetween>
  );
}

const EVENT_COLUMN_DEFINITIONS: readonly TableProps.ColumnDefinition<SimulatorEvent>[] =
  [
    {
      id: 'sequence',
      header: 'Sequence',
      cell: (event) => event.sequence,
    },
    {
      id: 'type',
      header: 'Type',
      cell: (event) => <Box variant="strong">{event.type}</Box>,
    },
    {
      id: 'timestamp',
      header: 'Virtual timestamp',
      cell: (event) => (
        <time dateTime={event.virtualTimestamp}>{event.virtualTimestamp}</time>
      ),
    },
    {
      id: 'command',
      header: 'Command',
      cell: (event) => (
        <Box variant="code">
          {event.command.operation} · {event.command.id}
        </Box>
      ),
    },
    {
      id: 'payload',
      header: 'Payload',
      cell: (event) => (
        <ExpandableSection headerText="Event payload">
          <Box variant="pre">{JSON.stringify(event.payload, null, 2)}</Box>
        </ExpandableSection>
      ),
    },
  ];

function EventTimeline({
  events,
}: {
  readonly events: readonly SimulatorEvent[];
}): React.JSX.Element {
  return (
    <Table
      columnDefinitions={EVENT_COLUMN_DEFINITIONS}
      empty={
        <Box color="text-body-secondary">
          No events exist after this cursor.
        </Box>
      }
      items={events.toReversed()}
      trackBy={(event) => String(event.sequence)}
      variant="embedded"
    />
  );
}

function diagnosticSource(diagnostic: SimulatorDiagnostic): string {
  const line = diagnostic.source?.line ? `:${diagnostic.source.line}` : '';
  return diagnostic.source ? `${diagnostic.source.file}${line}` : '—';
}

const DIAGNOSTIC_COLUMN_DEFINITIONS: readonly TableProps.ColumnDefinition<SimulatorDiagnostic>[] =
  [
    {
      id: 'code',
      header: 'Code',
      cell: (diagnostic) => (
        <StatusIndicator type="error">{diagnostic.code}</StatusIndicator>
      ),
    },
    {
      id: 'message',
      header: 'Message',
      cell: (diagnostic) => diagnostic.message,
    },
    {
      id: 'source',
      header: 'Source',
      cell: (diagnostic) => (
        <Box variant="code">{diagnosticSource(diagnostic)}</Box>
      ),
    },
  ];

function diagnosticKey(diagnostic: SimulatorDiagnostic): string {
  return `${diagnostic.code}:${diagnostic.provider}:${diagnostic.service}:${diagnostic.resourceType}:${diagnostic.operation}:${diagnostic.source?.file}:${diagnostic.source?.line}`;
}

function Diagnostics({
  entries,
}: {
  readonly entries: readonly SimulatorDiagnostic[];
}): React.JSX.Element {
  return (
    <Table
      columnDefinitions={DIAGNOSTIC_COLUMN_DEFINITIONS}
      empty={<Box color="text-body-secondary">No deployment diagnostics.</Box>}
      items={[...entries]}
      trackBy={diagnosticKey}
      variant="embedded"
    />
  );
}

function OutputList({
  outputs,
}: {
  readonly outputs: Readonly<Record<string, string>>;
}): React.JSX.Element {
  const entries = Object.entries(outputs);
  if (entries.length === 0) {
    return <Box color="text-body-secondary">No deployment outputs.</Box>;
  }
  return (
    <KeyValuePairs
      items={entries.map(([label, value]) => ({
        label,
        value: <Box variant="code">{value}</Box>,
      }))}
    />
  );
}

interface OperationFormValues {
  readonly provider: string;
  readonly targetId: string;
  readonly engine: string;
  readonly service: string;
  readonly resourceType: string;
  readonly operation: string;
  readonly input: string;
  readonly idempotencyKey: string;
}

interface OperationFieldDefinition {
  readonly label: string;
  readonly name: Exclude<keyof OperationFormValues, 'input' | 'idempotencyKey'>;
  readonly placeholder?: string;
}

const OPERATION_FIELD_DEFINITIONS: readonly OperationFieldDefinition[] = [
  { label: 'Provider', name: 'provider', placeholder: 'gcp' },
  { label: 'Target ID', name: 'targetId' },
  { label: 'Engine', name: 'engine', placeholder: 'infra-manager' },
  { label: 'Service', name: 'service', placeholder: 'run' },
  {
    label: 'Resource type',
    name: 'resourceType',
    placeholder: 'google_cloud_run_v2_service',
  },
  { label: 'Operation', name: 'operation', placeholder: 'UpdateService' },
];

function ProviderOperationForm({
  deploymentId,
  onOperation,
}: {
  readonly deploymentId: string;
  readonly onOperation: (formData: FormData) => Promise<void>;
}): React.JSX.Element {
  const idempotencyKey = useRef(`console-${crypto.randomUUID()}`).current;
  const [fields, setFields] = useState<OperationFormValues>({
    provider: '',
    targetId: 'default',
    engine: '',
    service: '',
    resourceType: '',
    operation: '',
    input: '{}',
    idempotencyKey,
  });
  const [actionState, formAction, pending] = useActionState(
    runConsoleOperationAction.bind(null, onOperation),
    INITIAL_OPERATION_STATE
  );
  const setField = (name: keyof OperationFormValues, value: string): void =>
    setFields((current) => ({ ...current, [name]: value }));
  return (
    <form action={formAction}>
      <Form
        actions={
          <Button disabled={pending} variant="primary">
            {pending ? 'Executing…' : 'Execute command'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <Box>
            Deployment{' '}
            <Box display="inline" variant="code">
              {deploymentId}
            </Box>
            . The command appends to this world and refreshes from its event
            stream.
          </Box>
          <ColumnLayout columns={2}>
            {OPERATION_FIELD_DEFINITIONS.map((field) => (
              <FormField key={field.name} label={field.label}>
                <Input
                  autoComplete={false}
                  name={field.name}
                  onChange={({ detail }) => setField(field.name, detail.value)}
                  value={fields[field.name]}
                  {...(field.placeholder
                    ? { placeholder: field.placeholder }
                    : {})}
                />
              </FormField>
            ))}
          </ColumnLayout>
          <FormField label="Input JSON object" stretch>
            <Textarea
              name="input"
              onChange={({ detail }) => setField('input', detail.value)}
              rows={7}
              value={fields.input}
            />
          </FormField>
          <FormField label="Idempotency key" stretch>
            <Input
              autoComplete={false}
              name="idempotencyKey"
              onChange={({ detail }) =>
                setField('idempotencyKey', detail.value)
              }
              value={fields.idempotencyKey}
            />
          </FormField>
          <ConsoleOperationResult state={actionState} />
        </SpaceBetween>
      </Form>
    </form>
  );
}

function ProviderOperationPanel({
  deploymentId,
  onOperation,
}: {
  readonly deploymentId?: string;
  readonly onOperation: (formData: FormData) => Promise<void>;
}): React.JSX.Element {
  return (
    <Container
      header={
        <Header description="Shared command API" variant="h2">
          Provider operation
        </Header>
      }
    >
      {deploymentId ? (
        <ProviderOperationForm
          deploymentId={deploymentId}
          onOperation={onOperation}
        />
      ) : (
        <Box color="text-body-secondary">
          Select a deployment in the Console URL before executing a command.
        </Box>
      )}
    </Container>
  );
}

function ReadyConsole({
  state,
  onRefresh,
  onOperation,
}: {
  readonly state: Extract<ConsoleLoadState, { readonly kind: 'ready' }>;
  readonly onRefresh: () => void;
  readonly onOperation: (formData: FormData) => Promise<void>;
}): React.JSX.Element {
  const { data } = state;
  const providers = groupResources(data.resources);
  const deploymentDiagnostics = diagnostics(data.deployment);
  return (
    <ContentLayout
      header={
        <Header
          actions={<Button onClick={onRefresh}>Refresh projection</Button>}
          description="One source of truth for every provider, command, and projected resource."
          variant="h1"
        >
          {data.worldId}
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Container>
          <KeyValuePairs
            columns={4}
            items={[
              { label: 'Providers', value: providers.length },
              {
                label: 'Resources',
                value: data.resources.resources.length,
              },
              { label: 'Events', value: data.events.length },
              {
                label: 'Deployment',
                value: data.deployment?.status ?? 'not selected',
              },
            ]}
          />
        </Container>
        <Grid
          gridDefinition={[
            { colspan: { default: 12, m: 8 } },
            { colspan: { default: 12, m: 4 } },
          ]}
        >
          <SpaceBetween size="m">
            <Header
              counter={`cursor ${data.cursor}`}
              description="Resource graph"
              variant="h2"
            >
              Provider projections
            </Header>
            <ResourceGraph resources={data.resources.resources} />
          </SpaceBetween>
          <SpaceBetween size="l">
            <ProviderOperationPanel
              {...(data.deployment
                ? { deploymentId: data.deployment.deploymentId }
                : {})}
              onOperation={onOperation}
            />
            <Container
              header={
                <Header
                  actions={
                    data.deployment ? (
                      <ConsoleStatus status={data.deployment.status} />
                    ) : null
                  }
                  description="Deployment"
                  variant="h2"
                >
                  Outputs
                </Header>
              }
            >
              <OutputList outputs={data.deployment?.outputs ?? {}} />
            </Container>
            <Container
              header={
                <Header
                  counter={`(${deploymentDiagnostics.length})`}
                  description="Preflight & runtime"
                  variant="h2"
                >
                  Diagnostics
                </Header>
              }
            >
              <Diagnostics entries={deploymentDiagnostics} />
            </Container>
          </SpaceBetween>
        </Grid>
        <Container
          header={
            <Header
              actions={<Box variant="strong">SSE replay</Box>}
              description="Replayable audit trail"
              variant="h2"
            >
              Event timeline
            </Header>
          }
        >
          <EventTimeline events={data.events} />
        </Container>
      </SpaceBetween>
    </ContentLayout>
  );
}

export function WorldConsoleView({
  state,
  onRefresh,
  onOperation,
}: WorldConsoleViewProps): React.JSX.Element {
  return (
    <div className="console-shell">
      <header className="console-header" id="console-header">
        <TopNavigation
          identity={{
            href: '/',
            title: 'TenkaCloud Simulator',
          }}
          utilities={[{ type: 'button', text: 'Protocol 2026-07-11' }]}
        />
      </header>
      <AppLayout
        headerSelector="#console-header"
        content={
          <>
            {state.kind === 'loading' ? (
              <LoadingState worldId={state.worldId} />
            ) : null}
            {state.kind === 'error' ? (
              <ErrorState
                message={state.message}
                onRefresh={onRefresh}
                worldId={state.worldId}
              />
            ) : null}
            {state.kind === 'ready' ? (
              <ReadyConsole
                onOperation={onOperation}
                onRefresh={onRefresh}
                state={state}
              />
            ) : null}
          </>
        }
        navigationHide
        toolsHide
      />
    </div>
  );
}
