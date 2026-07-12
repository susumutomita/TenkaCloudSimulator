import type { FidelityDimension } from '@tenkacloud/simulator-contracts';
import type { FidelityLevel } from '@tenkacloud/simulator-core';

const FIDELITY_DIMENSION_BY_LEVEL: Readonly<
  Record<FidelityLevel, FidelityDimension>
> = {
  L0: 'contract',
  L1: 'control',
  L2: 'security',
  L3: 'network',
  L4: 'data-plane',
};

export function fidelityDimensions(
  levels: readonly FidelityLevel[]
): readonly FidelityDimension[] {
  return Array.from(
    new Set(levels.map((level) => FIDELITY_DIMENSION_BY_LEVEL[level]))
  );
}
