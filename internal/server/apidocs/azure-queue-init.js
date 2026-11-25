"use strict";

(async () => {
  const prefix = "/_simulator/providers/azure-queue";
  const specURL = "/_simulator/docs/azure-queue/openapi.json";
  const local = { mode: "same-origin", credentials: "same-origin", redirect: "error", referrerPolicy: "no-referrer" };
  try {
    const selected = new URLSearchParams(location.search).getAll("principal");
    const principal = selected[0];
    if (selected.length > 1 || (principal !== undefined &&
        (principal.trim() !== principal || !/^[A-Za-z0-9_-]{1,64}$/.test(principal))))
      throw new Error("Invalid simulator exercise selection.");
    // This is a request to select an exercise identity, not a credential.
    // Access still authenticates the cookie and constrains identity selection.
    const selectedHeaders = principal === undefined ? {} : { "X-Simulator-Principal": principal };
    const response = await fetch(specURL, { ...local, headers: selectedHeaders });
    if (!response.ok) throw new Error("The local Queue Storage reference is unavailable.");
    const spec = await response.json();
    const account = spec.paths?.["/{account}/{queue}"]?.put?.parameters?.find(
      (item) => item.in === "path" && item.name === "account",
    )?.schema?.default;
    if (spec.servers?.[0]?.url !== prefix || !/^[a-z0-9]{3,24}$/.test(account ?? ""))
      throw new Error("The configured local storage account is unavailable.");

    const methods = [
      new Set(["PUT", "DELETE"]),
      new Set(["POST", "GET", "DELETE"]),
      new Set(["PUT", "DELETE"]),
    ];
    Scalar.createApiReference("#reference", {
      content: JSON.stringify(spec),
      withDefaultFonts: false,
      darkMode: false,
      hideDarkModeToggle: true,
      hideClientButton: true,
      showDeveloperTools: "never",
      telemetry: false,
      agent: { disabled: true },
      proxyUrl: "",
      persistAuth: false,
      customFetch: (input, init) => {
        const request = new Request(typeof input === "string" ? new URL(input, location.href) : input, init);
        const url = new URL(request.url);
        const base = prefix + "/" + account + "/";
        const parts = url.pathname.slice(base.length).split("/");
        const queue = /^[a-z0-9](?!.*--)[a-z0-9-]{1,61}[a-z0-9]$/.test(parts[0]);
        if (
          url.origin !== location.origin || url.username || url.password || url.hash ||
          !url.pathname.startsWith(base) || !queue ||
          !methods[parts.length - 1]?.has(request.method) ||
          (parts.length > 1 && parts[1] !== "messages") ||
          (parts.length === 3 && !/^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/.test(parts[2]))
        ) return Promise.reject(new Error("Only operations on this local storage account are allowed."));

        // Only the existing local cookie authenticates the caller. Keep the
        // same selected exercise identity and ignore Scalar-supplied credentials.
        const headers = new Headers(selectedHeaders);
        for (const name of ["accept", "content-type", "x-ms-version"]) {
          if (request.headers.has(name)) headers.set(name, request.headers.get(name));
        }
        return fetch(new Request(request, { ...local, headers }));
      },
    });
  } catch (error) {
    const message = document.createElement("p");
    message.setAttribute("role", "alert");
    message.textContent = error.message;
    document.getElementById("reference").append(message);
  }
})();
