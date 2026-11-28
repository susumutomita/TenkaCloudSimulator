"use strict";

(async () => {
  const spec = "/_simulator/docs/support.json";
  const prefix = "AWSSupport_20130415.";
  const operations = new Set(["AddAttachmentsToSet", "AddCommunicationToCase",
    "CreateCase", "DescribeAttachment", "DescribeCases", "DescribeCommunications", "ResolveCase"]);

  const definition = await fetch(spec, { credentials: "same-origin", redirect: "error" });
  if (!definition.ok) throw new Error("The local AWS Support reference could not be loaded.");
  const document = await definition.json();
  document.servers = [{ url: location.origin }];
  Scalar.createApiReference("#reference", {
    content: JSON.stringify(document),
    plugins: [SimulatorAWSJSONRenderer],
    withDefaultFonts: false,
    persistAuth: false,
    telemetry: false,
    agent: { disabled: true },
    proxyUrl: "",
    async customFetch(input, init) {
      const request = new Request(typeof input === "string" ? new URL(input, location.href) : input, init);
      const url = new URL(request.url);
      if (url.origin !== location.origin || url.username || url.password || url.search || url.hash)
        throw new Error("Use the current simulator's local AWS Support endpoint.");
      const options = { credentials: "same-origin", redirect: "error", signal: request.signal };
      if (request.method === "GET" && url.pathname === spec) return fetch(url, options);
      const target = request.headers.get("X-Amz-Target") || "";
      if (url.pathname !== "/" || request.method !== "POST" || !target.startsWith(prefix) ||
          !operations.has(target.slice(prefix.length)))
        throw new Error("Select a supported AWS Support operation.");
      return fetch(url, {
        ...options, method: "POST", body: await request.text(),
        headers: {
          "Content-Type": "application/x-amz-json-1.1", "X-Amz-Target": target,
          "X-Simulator-Service": "support", "X-Simulator-Region": "us-east-1"
        }
      });
    }
  });
})();
