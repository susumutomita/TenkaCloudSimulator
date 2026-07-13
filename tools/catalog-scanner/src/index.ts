export {
  compareInventory,
  coverageReportHash,
  readCapabilityManifest,
  serializeReport,
  validateCapabilityManifest,
} from './manifest.ts';
export type {
  CapabilityEntry,
  CapabilityManifest,
  CatalogInventory,
  CoverageReport,
  CoverageReportIdentity,
  Diagnostic,
  Fidelity,
  Requirement,
  RequirementClassification,
} from './model.ts';
export { collectCatalog } from './scanner.ts';
