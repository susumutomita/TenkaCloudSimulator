"use strict";

(() => {
  const specPath = "/_simulator/docs/cloudfront-local.json";
  const collection = "/2020-05-31/distribution";
  Scalar.createApiReference("#reference", {
    url: specPath,
    withDefaultFonts: false,
    persistAuth: false,
    telemetry: false,
    agent: { disabled: true },
    proxyUrl: "",
    customFetch: async (input, init) => {
      const request = new Request(typeof input === "string" ? new URL(input, location.href) : input, init);
      const url = new URL(request.url);
      if (url.origin !== location.origin || url.username || url.password || url.hash)
        throw new Error("Only this simulator's local CloudFront API is available.");
      if (request.method === "GET" && url.pathname === specPath && !url.search)
        return fetch(url, { credentials: "same-origin", redirect: "error", signal: request.signal });
      const suffix = url.pathname.slice(collection.length);
      const valid = url.pathname === collection ? ["GET", "POST"].includes(request.method)
        : url.pathname.startsWith(collection + "/") && (
          (/^\/D[a-f0-9]{24}$/.test(suffix) && ["GET", "DELETE"].includes(request.method)) ||
          (/^\/D[a-f0-9]{24}\/config$/.test(suffix) && ["GET", "PUT"].includes(request.method)) ||
          (/^\/D[a-f0-9]{24}\/invalidation$/.test(suffix) && request.method === "POST") ||
          (/^\/D[a-f0-9]{24}\/invalidation\/I[a-f0-9]{24}$/.test(suffix) && request.method === "GET"));
      if (!valid || (url.search && !(url.pathname === collection && request.method === "GET")))
        throw new Error("Unsupported local CloudFront method or path.");
      for (const [name] of url.searchParams)
        if (!["Marker", "MaxItems"].includes(name) || url.searchParams.getAll(name).length !== 1)
          throw new Error("Unsupported distribution list query.");
      const options = { method: request.method, credentials: "same-origin", redirect: "error", signal: request.signal,
        headers: { "X-Simulator-Service": "cloudfront", "X-Simulator-Region": "us-east-1" } };
      if (request.method === "POST" || request.method === "PUT") {
        options.headers["Content-Type"] = "application/xml";
        options.body = await request.text();
      }
      const match = request.headers.get("if-match");
      if (match !== null) options.headers["If-Match"] = match;
      return fetch(url, options);
    }
  });
})();
