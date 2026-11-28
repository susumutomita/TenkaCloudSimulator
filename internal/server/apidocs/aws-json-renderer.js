"use strict";

// Render native AWS JSON text without changing the response bytes or wire media type.
const SimulatorAWSJSONRenderer = () => ({
  name: "simulator-aws-json",
  extensions: [],
  apiClientPlugins: [{
    name: "simulator-aws-json",
    responseBody: [{
      mimeTypes: ["application/x-amz-json-1.0", "application/x-amz-json-1.1"],
      language: "json",
      decode: (bytes) => new TextDecoder("utf-8", { ignoreBOM: true }).decode(bytes),
    }],
  }],
});
