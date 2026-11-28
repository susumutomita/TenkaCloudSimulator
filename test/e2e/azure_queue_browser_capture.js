({path, query}) => {
  // Observe the real Fetch response before Chromium can discard its inspector copy.
  const canonical = value => {
    const url = new URL(value, location.href);
    url.searchParams.sort();
    return url.href;
  };
  const target = canonical(path + '?' + new URLSearchParams(query));
  const original = window.fetch;
  let matches = 0;
  let text = null;
  let done = false;

  async function read(response) {
    if (response.status !== 200 || response.redirected || canonical(response.url) !== target) return;
    const reader = response.clone().body.getReader();
    const decoder = new TextDecoder('utf-8', {fatal: true});
    const parts = [];
    let size = 0;
    try {
      for (;;) {
        const chunk = await reader.read();
        if (chunk.done) break;
        size += chunk.value.byteLength;
        if (size > 131072) {
          void reader.cancel().catch(() => {});
          return;
        }
        parts.push(decoder.decode(chunk.value, {stream: true}));
      }
      text = parts.join('') + decoder.decode();
    } finally {
      reader.releaseLock();
    }
  }

  function observe(input, init) {
    const pending = original.apply(this, arguments);
    const method = init?.method ?? (input instanceof Request ? input.method : 'GET');
    const url = input instanceof Request ? input.url : input;
    if (method === 'GET' && canonical(url) === target && ++matches === 1) {
      pending.then(read).then(() => { done = true; }, () => { done = true; });
    }
    return pending;
  }

  window.fetch = observe;
  return {
    get done() { return done; },
    get text() { return matches === 1 && done ? text : null; },
    stop() { window.fetch = original; }
  };
}
