const endpoint = '/_simulator/providers/gcp/cloudtrace/v1';
const reference = '/_simulator/docs/gcp-trace.html';
const projectID = /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/;
const traceID = /^[0-9a-f]{32}$/;
const stamp = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/;
const maxSpanID = '18446744073709551615';

// Discovery is presentation only. The native service independently checks its owner.
export function traceConfiguration(scope) {
  return scope && scope.console !== false && typeof scope.project === 'string' && projectID.test(scope.project)
    && scope.endpoint === endpoint && scope.reference === reference
    && Array.isArray(scope.methods) && ['get', 'list'].every(method => scope.methods.includes(method))
    && Array.isArray(scope.views) && scope.views.includes('MINIMAL') ? scope : null;
}

function validTraceID(value) {
  return typeof value === 'string' && traceID.test(value) && value !== '0'.repeat(32);
}
function decimalID(value) {
  return typeof value === 'string' && /^[1-9][0-9]{0,19}$/.test(value)
    && (value.length < maxSpanID.length || value <= maxSpanID);
}
function timestamp(value) { return typeof value === 'string' && stamp.test(value); }

export function spanObservations(spans) {
  if (!Array.isArray(spans)) throw Error('Invalid saved trace observations.');
  return spans.map((span, index) => {
    if (!span || !decimalID(span.spanId) || (span.parentSpanId !== undefined && span.parentSpanId !== ''
        && span.parentSpanId !== '0' && !decimalID(span.parentSpanId))
        || typeof span.name !== 'string' || !span.name || !timestamp(span.startTime) || !timestamp(span.endTime)
        || (span.kind !== undefined && typeof span.kind !== 'string')
        || (span.labels !== undefined && (!span.labels || Array.isArray(span.labels)
          || typeof span.labels !== 'object' || !Object.values(span.labels).every(value => typeof value === 'string')))) {
      throw Error('Invalid saved span. IDs must be exact decimal strings.');
    }
    // Observation identity is separate from spanId: repeated IDs are retained.
    return {observation: index + 1, spanId: span.spanId, parentSpanId: span.parentSpanId,
      name: span.name, kind: span.kind, startTime: span.startTime, endTime: span.endTime,
      labels: {...span.labels}};
  });
}

export function cloudTrace(request, capability) {
  const scope = traceConfiguration(capability);
  if (!scope) throw Error('Cloud Trace is unavailable for this identity.');
  const collection = endpoint + '/projects/' + encodeURIComponent(scope.project) + '/traces';
  async function read(path, signal) {
    signal?.throwIfAborted();
    const response = await request(path, {method: 'GET', redirect: 'error', signal});
    const data = await response.json();
    signal?.throwIfAborted();
    return data;
  }
  return {
    async list(range, pageToken = '', signal) {
      if (!timestamp(range?.startTime) || !timestamp(range?.endTime)) throw Error('Enter RFC 3339 start and end times.');
      if (typeof pageToken !== 'string' || pageToken.length > 2048) throw Error('Invalid trace page token.');
      const query = new URLSearchParams({startTime: range.startTime, endTime: range.endTime,
        view: 'MINIMAL', pageSize: '25'});
      if (pageToken) query.set('pageToken', pageToken);
      const result = await read(collection + '?' + query, signal);
      if (!Array.isArray(result?.traces)) throw Error('Invalid trace list response.');
      const seen = new Set();
      const traces = result.traces.map(trace => {
        if (trace?.projectId !== scope.project || !validTraceID(trace.traceId) || seen.has(trace.traceId)) {
          throw Error('Invalid trace identity in list response.');
        }
        seen.add(trace.traceId);
        return {projectId: trace.projectId, traceId: trace.traceId};
      });
      const next = result.nextPageToken;
      if (next !== undefined && (typeof next !== 'string' || !next || next.length > 2048)) {
        throw Error('Invalid trace page token response.');
      }
      return {traces, nextPageToken: next || ''};
    },
    async get(id, signal) {
      if (!validTraceID(id)) throw Error('Invalid trace ID.');
      const result = await read(collection + '/' + encodeURIComponent(id), signal);
      if (result?.projectId !== scope.project || result.traceId !== id) throw Error('Unexpected saved trace identity.');
      return {projectId: result.projectId, traceId: result.traceId, observations: spanObservations(result.spans)};
    },
  };
}
