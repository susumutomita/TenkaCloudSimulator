import {
  assertSimulatorEventPage,
  SIMULATOR_EVENT_PAGE_SIZE,
  type SimulatorEventPage,
} from '@tenkacloud/simulator-contracts';
import type { EventRecord } from '@tenkacloud/simulator-core';
import type { Context } from 'hono';
import { RequestValidationError } from './errors.js';
import { eventResponse } from './presenters.js';

export const MAX_EVENT_PAGE_SIZE = SIMULATOR_EVENT_PAGE_SIZE;
export const NEXT_CURSOR_HEADER = 'x-tenkacloud-next-cursor';

export function eventCursor(c: Context): number {
  const value =
    c.req.query('after') ?? c.req.header('last-event-id')?.trim() ?? '0';
  if (!/^\d+$/.test(value)) {
    throw new RequestValidationError(
      'event cursor must be a non-negative integer'
    );
  }
  const cursor = Number(value);
  if (!Number.isSafeInteger(cursor)) {
    throw new RequestValidationError('event cursor exceeds the safe range');
  }
  return cursor;
}

export function eventPage(
  events: readonly EventRecord[],
  after: number
): SimulatorEventPage {
  const replay = events
    .filter((event) => event.sequence > after)
    .slice(0, MAX_EVENT_PAGE_SIZE)
    .map(eventResponse);
  const response: SimulatorEventPage = {
    events: replay,
    nextCursor: replay.at(-1)?.sequence ?? after,
  };
  assertSimulatorEventPage(response);
  return response;
}
