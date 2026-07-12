# syntax=docker/dockerfile:1.7

ARG BUN_IMAGE=oven/bun:1.3.11-slim@sha256:478281fdd196871c7e51ba6a820b7803a8ae97042ec86cdbc2e1c6b6626442d9
ARG DOCKER_CLI_IMAGE=docker:29.1.3-cli@sha256:4fa0ee1f3a7e4354c4ea34558b6d4ee32859baf4973d4c8ccc8e7fe3dd730c04

FROM ${DOCKER_CLI_IMAGE} AS docker-cli

FROM ${BUN_IMAGE} AS build
WORKDIR /workspace
RUN chown bun:bun /workspace
COPY --chown=bun:bun . .
USER bun
RUN bun install --frozen-lockfile --ignore-scripts
RUN bun run --filter @tenkacloud/simulator-console build
RUN bun build apps/server/src/bin.ts \
  --target bun \
  --outfile /home/bun/server.js \
  --minify

FROM ${BUN_IMAGE} AS runtime
LABEL org.opencontainers.image.source="https://github.com/susumutomita/TenkaCloudSimulator" \
  org.opencontainers.image.description="Deterministic local multi-cloud simulator for TenkaCloud" \
  org.opencontainers.image.licenses="MIT"

USER root
RUN mkdir -p /app/console /var/lib/tenkacloud-simulator \
  && chown -R bun:bun /app /var/lib/tenkacloud-simulator
COPY --from=build --chown=bun:bun /home/bun/server.js /app/server.js
COPY --from=build --chown=bun:bun /workspace/apps/console/dist/ /app/console/
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker

ENV TENKACLOUD_SIMULATOR_CONSOLE_DIR=/app/console \
  TENKACLOUD_SIMULATOR_CONTAINER_MODE=1 \
  TENKACLOUD_SIMULATOR_HOST=0.0.0.0 \
  TENKACLOUD_SIMULATOR_PORT=7777 \
  TENKACLOUD_SIMULATOR_STATE_DIR=/var/lib/tenkacloud-simulator

EXPOSE 7777
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=5s --timeout=2s --start-period=5s --retries=6 \
  CMD ["bun", "-e", "const r=await fetch('http://127.0.0.1:7777/v1/capabilities');if(!r.ok)process.exit(1)"]
USER bun
ENTRYPOINT ["bun", "/app/server.js"]
