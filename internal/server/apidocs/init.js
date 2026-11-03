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
  customFetch: (input, init) => {
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
  },
});
