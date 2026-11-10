"use strict";
Scalar.createApiReference("#reference", {
  url: "/_simulator/docs/openapi.json",
  withDefaultFonts: false,
  theme: "default",
  darkMode: false,
  hideDarkModeToggle: true,
  hideClientButton: true,
  showDeveloperTools: "never",
  telemetry: false,
  agent: { disabled: true },
  proxyUrl: "",
  persistAuth: false,
  // AWS JSON 1.0/1.1 are JSON despite lacking the standard +json suffix.
  // Use Scalar's display decoder, not a fetch/header rewrite. Keep the raw
  // text (including whitespace and large numbers) and its original MIME type.
  plugins: [() => ({
    name: "simulator-aws-json",
    extensions: [],
    apiClientPlugins: [{
      name: "simulator-aws-json",
      responseBody: [{
        mimeTypes: ["application/x-amz-json-1.0", "application/x-amz-json-1.1"],
        language: "json",
        decode: (bytes) => new TextDecoder("utf-8", { ignoreBOM: true }).decode(bytes),
      }],
    }],
  })],
  customFetch: SimulatorS3ResponseGuide.wrapFetch((input, init) => {
    const url = new URL(
      typeof input === "string"
        ? input
        : input instanceof URL
          ? input.href
          : input.url,
      location.href,
    );
    if (url.origin !== location.origin)
      return Promise.reject(
        new Error("Only this simulator origin is allowed."),
      );
    return fetch(input, init);
  }),
});
