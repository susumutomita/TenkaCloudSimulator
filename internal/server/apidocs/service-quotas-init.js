"use strict";

(async () => {
  const spec = "/_simulator/docs/catalog/service-quotas.json";
  const prefix = "ServiceQuotasV20190624.";
  const operations = new Set([
    "GetAWSDefaultServiceQuota", "GetRequestedServiceQuotaChange", "GetServiceQuota",
    "ListAWSDefaultServiceQuotas", "ListRequestedServiceQuotaChangeHistory",
    "ListRequestedServiceQuotaChangeHistoryByQuota", "ListServiceQuotas", "ListServices",
    "RequestServiceQuotaIncrease",
  ]);
  const definition = await fetch(spec, { credentials: "same-origin", redirect: "error" });
  if (!definition.ok) throw new Error("The local Service Quotas reference could not be loaded.");
  const document = await definition.json();
  document.servers = [{ url: location.origin }];
  Scalar.createApiReference("#reference", {
    content: JSON.stringify(document),
    plugins: [SimulatorAWSJSONRenderer],
    withDefaultFonts: false,
    theme: "default",
    darkMode: false,
    hideDarkModeToggle: true,
    hideClientButton: true,
    showDeveloperTools: "never",
    persistAuth: false,
    telemetry: false,
    agent: { disabled: true },
    proxyUrl: "",
    async customFetch(input, init) {
      const request = new Request(typeof input === "string" ? new URL(input, location.href) : input, init);
      const url = new URL(request.url);
      if (url.origin !== location.origin || url.username || url.password || url.search || url.hash)
        throw new Error("Use the current simulator's local Service Quotas endpoint.");
      if (request.method === "GET" && url.href === location.origin + spec)
        return fetch(request, { credentials: "same-origin", redirect: "error" });
      const target = request.headers.get("X-Amz-Target") || "";
      if (url.href !== location.origin + "/" || request.method !== "POST" ||
          !target.startsWith(prefix) || !operations.has(target.slice(prefix.length)) ||
          request.headers.get("Content-Type")?.split(";", 1)[0].trim() !== "application/x-amz-json-1.1")
        throw new Error("Select a supported Service Quotas target and its JSON 1.1 request body.");
      if (["Authorization", "X-Simulator-Token", "X-Simulator-Principal", "X-Simulator-Access-Role"]
          .some((header) => request.headers.has(header)))
        throw new Error("Use your current local sign-in for this reference.");
      // Preserve the selected native target and body exactly. Body contents do
      // not select an operation, region or principal; native validation decides.
      return fetch(request, { credentials: "same-origin", redirect: "error" });
    },
  });
})();
