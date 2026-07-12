const TOKEN_PREFIX = 'tc_sim_v1.';
const MAX_TOKEN_LENGTH = 4096;
const TOKEN_PATTERN = /^tc_sim_v1\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/;

export class ConsoleLaunchTokenError extends Error {
  public constructor(message: string) {
    super(message);
    this.name = 'ConsoleLaunchTokenError';
  }
}

export function simulatorLaunchToken(value: string | null): string {
  if (value === null || !value) {
    throw new ConsoleLaunchTokenError(
      'A simulator launch token is required to open this world.'
    );
  }
  if (
    value.length > MAX_TOKEN_LENGTH ||
    !value.startsWith(TOKEN_PREFIX) ||
    !TOKEN_PATTERN.test(value)
  ) {
    throw new ConsoleLaunchTokenError(
      'The launch link does not contain a valid simulator token.'
    );
  }
  return value;
}

export interface ConsoleLaunch {
  readonly token: string;
  readonly cleanPath: string;
}

export function consumeLaunchToken(
  url: URL,
  replaceHistory: (cleanPath: string) => void
): ConsoleLaunch {
  const fragment = new URLSearchParams(url.hash.slice(1));
  const candidates = fragment.getAll('token');
  const candidate = candidates.length === 1 ? candidates[0] : null;
  url.hash = '';
  const cleanPath = `${url.pathname}${url.search}`;
  replaceHistory(cleanPath);
  return {
    token: simulatorLaunchToken(candidate ?? null),
    cleanPath,
  };
}
