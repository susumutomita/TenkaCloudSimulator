"use strict";
// One OpenAPI document per catalog service, generated from the embedded
// official botocore, Google discovery or Azure REST API specification subsets. The page names its catalog directory from a fixed
// allowlist; requests stay on this simulator origin.
const catalogDirectory = ["gcp-catalog", "azure-catalog"].includes(
  document.getElementById("reference").dataset.catalog,
)
  ? document.getElementById("reference").dataset.catalog
  : "catalog";
fetch("/_simulator/docs/" + catalogDirectory + "/index.json", {
  credentials: "same-origin",
})
  .then((response) => {
    if (!response.ok) throw new Error("Catalog reference index unavailable.");
    return response.json();
  })
  .then((services) => {
    Scalar.createApiReference("#reference", {
      sources: services.map((service, index) => ({
        title: service.title,
        slug: service.model,
        url:
          "/_simulator/docs/" +
          catalogDirectory +
          "/" +
          service.model +
          ".json",
        default: index === 0,
      })),
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
  })
  .catch((error) => {
    const message = document.createElement("p");
    message.setAttribute("role", "alert");
    message.textContent = error.message;
    document.getElementById("reference").append(message);
  });
