const SHA256_MARKER = '@sha256:';
const SHA256_HEX_LENGTH = 64;
const MAX_IMAGE_REFERENCE_LENGTH = 512;

function isLowercaseHexadecimal(character: string): boolean {
  return (
    (character >= '0' && character <= '9') ||
    (character >= 'a' && character <= 'f')
  );
}

function isLowercaseAlphaNumeric(character: string): boolean {
  return (
    (character >= '0' && character <= '9') ||
    (character >= 'a' && character <= 'z')
  );
}

export function sha256DigestPinnedImageName(value: string): string | undefined {
  if (value.length > MAX_IMAGE_REFERENCE_LENGTH) return undefined;
  const digestOffset = value.length - SHA256_HEX_LENGTH;
  const markerOffset = digestOffset - SHA256_MARKER.length;
  if (
    markerOffset < 1 ||
    value.slice(markerOffset, digestOffset) !== SHA256_MARKER
  ) {
    return undefined;
  }
  for (let index = digestOffset; index < value.length; index++) {
    if (!isLowercaseHexadecimal(value[index] ?? '')) return undefined;
  }
  return value.slice(0, markerOffset);
}

export function hasSha256DigestPin(value: string): boolean {
  return sha256DigestPinnedImageName(value) !== undefined;
}

export function isLowercaseDigestPinnedImage(value: string): boolean {
  const name = sha256DigestPinnedImageName(value);
  if (name === undefined) return false;
  let segmentStart = true;
  for (const character of name) {
    if (segmentStart) {
      if (!isLowercaseAlphaNumeric(character)) return false;
      segmentStart = false;
      continue;
    }
    if (character === '/') {
      segmentStart = true;
      continue;
    }
    if (
      !isLowercaseAlphaNumeric(character) &&
      character !== '.' &&
      character !== '_' &&
      character !== '-'
    ) {
      return false;
    }
  }
  return !segmentStart;
}
