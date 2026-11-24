"use strict";

(() => {
  const reference = document.getElementById("reference");
  const provider = "/_simulator/providers/gcp";
  const server = provider + "/dns/v1";
  const operation = "/projects/{project}/managedZones";
  const origin = location.origin;

  const readJSON = async (path) => {
    const response = await fetch(path, { credentials: "same-origin", redirect: "error" });
    if (!response.ok) throw new Error("Local Cloud DNS reference unavailable.");
    return response.json();
  };

  Promise.all([
    readJSON("/_simulator/v1/capabilities"),
    readJSON("/_simulator/docs/gcp-dns/dns.v1.json"),
  ])
    .then(([capabilities, specification]) => {
      const gcp = capabilities.cloudStorage?.gcp;
      const project = gcp?.project;
      if (
        gcp?.endpoint !== provider ||
        typeof project !== "string" ||
        !/^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(project)
      )
        throw new Error("Configured local GCP project unavailable.");

      const parameter = specification.paths[operation].get.parameters.find(
        (item) => item.in === "path" && item.name === "project",
      );
      parameter.schema.default = project;
      parameter.example = project;

      Scalar.createApiReference("#reference", {
        content: JSON.stringify(specification),
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
            typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
            location.href,
          );
          const method = init?.method ?? input?.method ?? "GET";
          const ownList = server + "/projects/" + project + "/managedZones";
          if (
            url.origin !== origin ||
            url.pathname !== ownList ||
            url.username ||
            url.password ||
            url.hash ||
            method !== "GET"
          )
            return Promise.reject(new Error("Only listing zones in this local project is allowed."));

          // Let the existing same-origin browser cookie identify the caller.
          // Do not forward custom credentials or follow redirects.
          return fetch(url, {
            method: "GET",
            credentials: "same-origin",
            redirect: "error",
            signal: init?.signal ?? input?.signal,
          });
        },
      });
    })
    .catch((error) => {
      const message = document.createElement("p");
      message.setAttribute("role", "alert");
      message.textContent = error.message;
      reference.append(message);
    });
})();
