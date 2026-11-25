"use strict";

(() => {
  const specPath = "/_simulator/docs/sns/http-subscriptions.json";
  const allowedFields = new Set(["Action", "Version", "TopicArn", "Protocol", "Endpoint"]);
  Scalar.createApiReference("#reference", {
    url: specPath,
    withDefaultFonts: false,
    darkMode: false,
    hideDarkModeToggle: true,
    hideClientButton: true,
    showDeveloperTools: "never",
    telemetry: false,
    agent: { disabled: true },
    proxyUrl: "",
    persistAuth: false,
    customFetch: async (input, init) => {
      const request = new Request(typeof input === "string" ? new URL(input, location.href) : input, init);
      const url = new URL(request.url);
      const deny = () => new Error("Only the simulator's local SNS subscription reference is allowed.");
      if (url.origin !== location.origin || url.search || url.hash || url.username || url.password) throw deny();
      if (request.method === "GET" && url.pathname === specPath) {
        return fetch(url, { credentials: "same-origin", redirect: "error", signal: request.signal });
      }
      const contentType = request.headers.get("content-type")?.split(";")[0].trim().toLowerCase();
      if (request.method !== "POST" || url.pathname !== "/" || contentType !== "application/x-www-form-urlencoded")
        throw deny();
      const body = await request.text();
      const fields = new URLSearchParams(body);
      const region = "us-east-1";
      if (body.length > 16384 || fields.size !== allowedFields.size ||
          [...allowedFields].some((key) => fields.getAll(key).length !== 1) ||
          fields.get("Action") !== "Subscribe" || fields.get("Version") !== "2010-03-31" ||
          !["http", "https"].includes(fields.get("Protocol")) ||
          !fields.get("TopicArn").startsWith("arn:aws:sns:" + region + ":000000000000:"))
        throw deny();

      // Leave endpoint validation to the native handler. Endpoint is inert
      // metadata, and browser-supplied authorization/routing never goes out.
      return fetch(url, {
        method: "POST", credentials: "same-origin", redirect: "error",
        headers: {
          "content-type": "application/x-www-form-urlencoded",
          "x-simulator-service": "sns",
          "x-simulator-region": region,
        },
        body, signal: request.signal,
      });
    },
  });
})();
