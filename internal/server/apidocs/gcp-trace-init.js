"use strict";

(async () => {
  const root = document.getElementById("reference");
  const server = "/_simulator/providers/gcp/cloudtrace/v1";
  const collection = "/projects/{projectId}/traces";
  const localJSON = async (path) => {
    const response = await fetch(path, {credentials: "same-origin", redirect: "error"});
    if (!response.ok) throw new Error("Cloud Trace reference unavailable.");
    return response.json();
  };
  try {
    const [caps, spec] = await Promise.all([
      localJSON("/_simulator/v1/capabilities"),
      localJSON("/_simulator/docs/gcp-trace/cloudtrace.v1.json")
    ]);
    const project = caps.gcpTrace?.project;
    if (caps.gcpTrace?.endpoint !== server || !/^[a-z][a-z0-9-]{4,28}[a-z0-9]$/.test(project ?? "")) {
      throw new Error("The configured local project owner is required.");
    }
    const id = "18ef054b00874c78a43cedf89eaf0042";
    const now = Date.now();
    const defaults = {
      projectId: project, traceId: id,
      startTime: new Date(now - 3600000).toISOString(),
      endTime: new Date(now + 60000).toISOString()
    };
    for (const methods of Object.values(spec.paths)) {
      for (const operation of Object.values(methods)) {
        for (const parameter of operation.parameters) {
          if (Object.hasOwn(defaults, parameter.name)) {
            parameter.example = defaults[parameter.name];
            parameter.schema.default = defaults[parameter.name];
          }
        }
      }
    }
    spec.paths[collection].patch.requestBody.content["application/json"].example = {
      traces: [{projectId: project, traceId: id, spans: [{
        spanId: "9007199254740993", name: "local request", kind: "RPC_SERVER",
        startTime: new Date(now - 1000).toISOString(), endTime: new Date(now).toISOString()
      }]}]
    };
    const base = server + "/projects/" + project + "/traces";
    Scalar.createApiReference("#reference", {
      content: JSON.stringify(spec),
      withDefaultFonts: false,
      theme: "default",
      persistAuth: false,
      customFetch: async (input, init = {}) => {
        const request = new Request(input, init);
        const url = new URL(request.url, location.origin);
        const record = url.pathname.startsWith(base + "/") && /^[0-9a-f]{32}$/.test(url.pathname.slice(base.length + 1));
        if (url.origin !== location.origin || url.username || url.password || url.hash ||
            !((url.pathname === base && ["GET","PATCH"].includes(request.method)) ||
              (record && request.method === "GET"))) {
          throw new Error("Unsupported local Cloud Trace destination.");
        }
        const options = {method: request.method, credentials: "same-origin", redirect: "error", signal: request.signal};
        if (request.method === "PATCH") {
          options.headers = {"Content-Type": "application/json"};
          options.body = await request.text();
        }
        return fetch(url.href, options);
      }
    });
  } catch (error) {
    const message = document.createElement("p");
    message.setAttribute("role", "alert");
    message.textContent = error.message;
    root.replaceChildren(message);
  }
})();
