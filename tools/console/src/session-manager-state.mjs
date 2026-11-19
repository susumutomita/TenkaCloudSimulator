// Presentation state for the console Session Manager page. The server decides
// connectability; the browser never infers it from stack state or URL registration.
export const READINESS_INTERVAL_MS = 5000;
export function connectionState(readiness) {
  if (readiness === null || readiness === undefined) return 'checking';
  if (readiness === 'unavailable') return 'unavailable';
  if (readiness.Connectable === true) return 'ready';
  if (readiness.Connectable === false) return 'preparing';
  return 'unavailable';
}
// Start is enabled only when the server reports the guest relay as answering and
// no session is already retained by this page.
export function canStart(state, phase) { return state === 'ready' && phase === 'idle'; }
export function validReadiness(value, scope) {
  return !!value && typeof value.Connectable === 'boolean' && value.Target === scope?.instanceId && value.Region === scope?.region;
}
