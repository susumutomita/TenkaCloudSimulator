import { describe, expect, it } from 'bun:test';
import * as CoreApi from './index';

describe('core public entrypoint の契約', () => {
  it('利用者向け runtime API を index から漏れなく公開する', () => {
    expect(Object.keys(CoreApi).sort()).toEqual([
      'CoreError',
      'FIDELITY_LEVELS',
      'HTTP_ENDPOINT_RESOURCE',
      'MAX_PROVIDER_HTTP_BODY_BYTES',
      'ProviderRegistry',
      'SimulationCore',
      'SimulationStore',
      'canonicalJson',
      'contentHash',
      'deterministicId',
      'providerHttpRequest',
      'providerHttpResponse',
      'singleReadyDeploymentResource',
    ]);
  });

  it('fidelity level を L0 から L4 の順で固定する', () => {
    expect(CoreApi.FIDELITY_LEVELS).toEqual(['L0', 'L1', 'L2', 'L3', 'L4']);
  });
});
