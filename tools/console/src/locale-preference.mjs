// First-party presentation preference shared with the local API reference.
// Events carry only an implemented locale, never authority, API or user data.
export const preferenceKey = 'tenkacloud-simulator-locale';
export const localeChangeEvent = 'tenkacloud-simulator-locale-change';
export const isConsoleLocale = value => value === 'en' || value === 'ja';

export function readLocalePreference(getStorage) {
  try { return getStorage().getItem(preferenceKey) === 'ja' ? 'ja' : 'en'; }
  catch { return 'en'; }
}

export function writeLocalePreference(getStorage, locale) {
  if (!isConsoleLocale(locale)) return false;
  try { getStorage().setItem(preferenceKey, locale); return true; }
  catch { return false; } // Storage can be unavailable; local presentation still works.
}

export function publishLocalePreference(target, getStorage, locale) {
  if (!isConsoleLocale(locale)) return false;
  writeLocalePreference(getStorage, locale);
  target.dispatchEvent(new target.CustomEvent(localeChangeEvent, {detail:{locale}}));
  return true;
}

export function subscribeLocalePreference(target, getStorage, onLocale) {
  function storageChanged(event) {
    if (event.key !== preferenceKey && event.key !== null) return;
    if (event.key === null && event.newValue !== null) return;
    if (event.newValue !== null && !isConsoleLocale(event.newValue)) return;
    try {
      const storage = getStorage();
      if (event.storageArea !== storage) return;
      // Queued events can describe an older write. Apply the current preference.
      const value = storage.getItem(preferenceKey);
      if (value === null || isConsoleLocale(value)) onLocale(value === null ? 'en' : value);
    } catch { /* Ignore inaccessible storage without rewriting the current locale. */ }
  }
  function localChanged(event) {
    const detail = event.detail;
    if (detail && typeof detail === 'object' && Object.getPrototypeOf(detail) === Object.prototype &&
        Object.hasOwn(detail, 'locale') && Reflect.ownKeys(detail).length === 1 &&
        isConsoleLocale(detail.locale)) onLocale(detail.locale);
  }
  target.addEventListener('storage', storageChanged);
  target.addEventListener(localeChangeEvent, localChanged);
  return () => {
    target.removeEventListener('storage', storageChanged);
    target.removeEventListener(localeChangeEvent, localChanged);
  };
}
