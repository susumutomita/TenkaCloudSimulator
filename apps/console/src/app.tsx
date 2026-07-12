import { useEffect, useState } from 'react';
import type { SimulatorConsoleClient } from './client';
import {
  type ConsoleRoute,
  loadConsoleData,
  parseConsoleRoute,
} from './loader';
import { type ConsoleLoadState, mergeEvents } from './model';
import { WorldConsoleView } from './view';

function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return 'The console could not load this world.';
}

function useConsoleWorld(
  client: SimulatorConsoleClient,
  route: ConsoleRoute
): readonly [ConsoleLoadState, () => void] {
  const [generation, setGeneration] = useState(0);
  const [streamGeneration, setStreamGeneration] = useState(0);
  const [state, setState] = useState<ConsoleLoadState>({
    kind: 'loading',
    worldId: route.worldId,
  });

  useEffect(() => {
    const controller = new AbortController();
    setState({ kind: 'loading', worldId: route.worldId });
    void loadConsoleData(client, route, controller.signal).then(
      (data) => setState({ kind: 'ready', data }),
      (error: unknown) => {
        if (!controller.signal.aborted) {
          setState({
            kind: 'error',
            worldId: route.worldId,
            message: errorMessage(error),
          });
        }
      }
    );
    return () => controller.abort(generation);
  }, [client, generation, route]);

  const eventCursor = state.kind === 'ready' ? state.data.cursor : undefined;
  useEffect(() => {
    if (eventCursor === undefined) return;
    const controller = new AbortController();
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    void client.stream(route.worldId, eventCursor, controller.signal).then(
      (batch) => {
        if (controller.signal.aborted) return;
        setState((current) =>
          current.kind === 'ready'
            ? {
                kind: 'ready',
                data: {
                  ...current.data,
                  events: mergeEvents(current.data.events, batch.events),
                  cursor: batch.nextCursor,
                },
              }
            : current
        );
        reconnectTimer = setTimeout(
          () => setStreamGeneration((current) => current + 1),
          1500
        );
      },
      (error: unknown) => {
        if (!controller.signal.aborted) {
          setState({
            kind: 'error',
            worldId: route.worldId,
            message: errorMessage(error),
          });
        }
      }
    );
    return () => {
      controller.abort(streamGeneration);
      if (reconnectTimer !== undefined) clearTimeout(reconnectTimer);
    };
  }, [client, eventCursor, route.worldId, streamGeneration]);

  return [state, () => setGeneration((current) => current + 1)];
}

function ConnectedConsole({
  client,
  route,
}: {
  readonly client: SimulatorConsoleClient;
  readonly route: ConsoleRoute;
}): React.JSX.Element {
  const [state, refresh] = useConsoleWorld(client, route);
  return <WorldConsoleView state={state} onRefresh={refresh} />;
}

export type ConsoleBootstrap =
  | {
      readonly kind: 'connected';
      readonly client: SimulatorConsoleClient;
      readonly pageUrl: URL;
    }
  | { readonly kind: 'error'; readonly error: unknown; readonly pageUrl: URL };

export function SimulatorConsoleApp({
  bootstrap,
}: {
  readonly bootstrap: ConsoleBootstrap;
}): React.JSX.Element {
  if (bootstrap.kind === 'error') {
    return (
      <WorldConsoleView
        state={{
          kind: 'error',
          worldId: 'unknown',
          message: errorMessage(bootstrap.error),
        }}
        onRefresh={() => globalThis.location.reload()}
      />
    );
  }
  try {
    const route = parseConsoleRoute(bootstrap.pageUrl);
    return <ConnectedConsole client={bootstrap.client} route={route} />;
  } catch (error) {
    return (
      <WorldConsoleView
        state={{
          kind: 'error',
          worldId: 'unknown',
          message: errorMessage(error),
        }}
        onRefresh={() => globalThis.location.reload()}
      />
    );
  }
}
