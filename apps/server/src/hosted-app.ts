import { lstat, realpath } from 'node:fs/promises';
import { extname, relative, resolve, sep } from 'node:path';
import type { Hono } from 'hono';
import { Hono as HonoApp } from 'hono';

const CONTENT_TYPES: Readonly<Record<string, string>> = {
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
};

function inside(root: string, path: string): boolean {
  const child = relative(root, path);
  return child === '' || (!child.startsWith(`..${sep}`) && child !== '..');
}

async function safeFile(
  root: string,
  requestPath: string
): Promise<string | undefined> {
  let decoded: string;
  try {
    decoded = decodeURIComponent(requestPath);
  } catch {
    return undefined;
  }
  const candidate = resolve(root, decoded.replace(/^\/+/, ''));
  if (!inside(root, candidate)) return undefined;
  const stat = await lstat(candidate).catch(() => undefined);
  if (!stat?.isFile() || stat.isSymbolicLink()) return undefined;
  const canonical = await realpath(candidate);
  return inside(root, canonical) ? canonical : undefined;
}

async function assetResponse(
  root: string,
  requestPath: string
): Promise<Response> {
  const path = await safeFile(root, requestPath);
  if (!path) return new Response('Not Found', { status: 404 });
  return new Response(Bun.file(path), {
    headers: {
      'content-type':
        CONTENT_TYPES[extname(path)] ?? 'application/octet-stream',
      'x-content-type-options': 'nosniff',
    },
  });
}

export function createHostedSimulatorApp(
  simulator: Hono,
  consoleDirectory: string,
  nativeGateway?: (request: Request) => Promise<Response | undefined>
): Hono {
  const app = new HonoApp();
  app.get('/assets/*', (c) =>
    assetResponse(consoleDirectory, c.req.path.slice(1))
  );
  app.get('/console', () => assetResponse(consoleDirectory, 'index.html'));
  app.get('/console/*', (c) => {
    const suffix = c.req.path.slice('/console/'.length);
    return extname(suffix)
      ? assetResponse(consoleDirectory, suffix)
      : assetResponse(consoleDirectory, 'index.html');
  });
  if (nativeGateway) {
    app.use('*', async (c, next) => {
      const response = await nativeGateway(c.req.raw);
      if (response) return response;
      await next();
    });
  }
  app.route('/', simulator);
  return app;
}
