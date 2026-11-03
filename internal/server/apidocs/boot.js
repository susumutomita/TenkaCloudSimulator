'use strict';
// The bundled Zod 4.6.7 supports jitless configuration. Set it before Scalar
// loads so its optional new Function capability probe respects the strict CSP.
globalThis.__zod_globalConfig = { jitless: true };
