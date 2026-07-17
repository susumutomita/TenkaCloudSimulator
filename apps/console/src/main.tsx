import '@cloudscape-design/global-styles/index.css';
import { applyMode, Mode } from '@cloudscape-design/global-styles';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { type ConsoleBootstrap, SimulatorConsoleApp } from './app';
import { SimulatorConsoleClient } from './client';
import { consumeLaunchToken } from './launch-token';
import './styles.css';

// OS のカラースキーム設定に追従する。判定できない環境では旧 bespoke テーマと
// 視覚的連続性のある dark を既定にする。
const prefersLight =
  window.matchMedia?.('(prefers-color-scheme: light)').matches ?? false;
applyMode(prefersLight ? Mode.Light : Mode.Dark);

const root = document.getElementById('root');
if (!root) throw new Error('Console root element was not found');

const pageUrl = new URL(globalThis.location.href);
let bootstrap: ConsoleBootstrap;
try {
  const launch = consumeLaunchToken(pageUrl, (cleanPath) =>
    globalThis.history.replaceState(globalThis.history.state, '', cleanPath)
  );
  const configuredBase = import.meta.env.VITE_SIMULATOR_API_BASE_URL;
  const apiBase = configuredBase ? configuredBase : pageUrl.origin;
  bootstrap = {
    kind: 'connected',
    client: new SimulatorConsoleClient(apiBase, launch.token),
    pageUrl,
  };
} catch (error) {
  bootstrap = { kind: 'error', error, pageUrl };
}

createRoot(root).render(
  <StrictMode>
    <SimulatorConsoleApp bootstrap={bootstrap} />
  </StrictMode>
);
