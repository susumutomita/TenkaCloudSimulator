#!/usr/bin/env bun
import { createSimulatorRuntime } from './runtime';

const runtime = await createSimulatorRuntime({
  ...(process.env['TENKACLOUD_SIMULATOR_AWS_ACCESS_KEY_ID'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_AWS_ACCESS_KEY_ID:
          process.env['TENKACLOUD_SIMULATOR_AWS_ACCESS_KEY_ID'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_AZURE_CREDENTIAL'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_AZURE_CREDENTIAL:
          process.env['TENKACLOUD_SIMULATOR_AZURE_CREDENTIAL'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_CONSOLE_DIR'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_CONSOLE_DIR:
          process.env['TENKACLOUD_SIMULATOR_CONSOLE_DIR'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_CONTAINER_MODE'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_CONTAINER_MODE:
          process.env['TENKACLOUD_SIMULATOR_CONTAINER_MODE'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_HOST'] === undefined
    ? {}
    : { TENKACLOUD_SIMULATOR_HOST: process.env['TENKACLOUD_SIMULATOR_HOST'] }),
  ...(process.env['TENKACLOUD_SIMULATOR_GCP_CREDENTIAL'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_GCP_CREDENTIAL:
          process.env['TENKACLOUD_SIMULATOR_GCP_CREDENTIAL'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_LAUNCH_SECRET'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_LAUNCH_SECRET:
          process.env['TENKACLOUD_SIMULATOR_LAUNCH_SECRET'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_PORT'] === undefined
    ? {}
    : { TENKACLOUD_SIMULATOR_PORT: process.env['TENKACLOUD_SIMULATOR_PORT'] }),
  ...(process.env['TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN:
          process.env['TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_SAKURA_CREDENTIAL'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_SAKURA_CREDENTIAL:
          process.env['TENKACLOUD_SIMULATOR_SAKURA_CREDENTIAL'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_STATE_DIR'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_STATE_DIR:
          process.env['TENKACLOUD_SIMULATOR_STATE_DIR'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES:
          process.env['TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER'] ===
  undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER:
          process.env['TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MEMORY_BYTES'] ===
  undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MEMORY_BYTES:
          process.env['TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MEMORY_BYTES'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MILLI_CPU'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MILLI_CPU:
          process.env['TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MILLI_CPU'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_WORKLOAD_MAX_PIDS'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_WORKLOAD_MAX_PIDS:
          process.env['TENKACLOUD_SIMULATOR_WORKLOAD_MAX_PIDS'],
      }),
  ...(process.env['TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE'] === undefined
    ? {}
    : {
        TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE:
          process.env['TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE'],
      }),
});
const server = Bun.serve({
  hostname: runtime.host,
  port: runtime.port,
  fetch: runtime.fetch,
  websocket: runtime.websocket,
});

process.on('SIGINT', async () => {
  await server.stop(true);
  runtime.close();
  process.exitCode = 0;
});

process.on('SIGTERM', async () => {
  await server.stop(true);
  runtime.close();
  process.exitCode = 0;
});
