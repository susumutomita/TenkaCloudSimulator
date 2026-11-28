"use strict";

(() => {
  const specPath = "/_simulator/docs/codestar-notifications.json";
  const paths = new Set(["/createNotificationRule", "/describeNotificationRule", "/listNotificationRules", "/updateNotificationRule", "/deleteNotificationRule"]);
  const regions = new Set(["us-east-1", "us-west-2", "ap-northeast-1", "eu-west-1"]);
  Scalar.createApiReference("#reference", {
    url: specPath,
    withDefaultFonts: false,
    hideClientButton: true,
    showDeveloperTools: "never",
    telemetry: false,
    agent: { disabled: true },
    proxyUrl: "",
    persistAuth: false,
    customFetch: async (input, init) => {
      const request = new Request(typeof input === "string" ? new URL(input, location.href) : input, init);
      const url = new URL(request.url);
      const deny = () => new Error("Select a documented operation on the local notification endpoint.");
      if (url.origin !== location.origin || url.search || url.hash || url.username || url.password) throw deny();
      const options = { credentials: "same-origin", mode: "same-origin", redirect: "error", signal: request.signal };
      if (request.method === "GET" && url.pathname === specPath) return fetch(url, options);
      const region = request.headers.get("x-simulator-region") || "us-east-1";
      if (request.method !== "POST" || !paths.has(url.pathname) || !regions.has(region)) throw deny();
      const body = await request.text();
      if (request.headers.get("content-type")?.split(";")[0] !== "application/json" ||
          new TextEncoder().encode(body).length > 65536) throw deny();
      return fetch(url, {
        ...options, method: "POST", body,
        headers: { "content-type": "application/json", "x-simulator-service": "codestar-notifications", "x-simulator-region": region },
      });
    },
  });
})();
