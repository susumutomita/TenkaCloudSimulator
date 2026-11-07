import React, { createContext, useContext, useEffect, useState } from 'react';
import { I18nProvider } from '@cloudscape-design/components/i18n';
import enMessages from '@cloudscape-design/components/i18n/messages/all.en';
import jaMessages from '@cloudscape-design/components/i18n/messages/all.ja';
import {isConsoleLocale, readLocalePreference, writeLocalePreference,
  publishLocalePreference, subscribeLocalePreference} from './locale-preference.mjs';

const LocaleContext = createContext({ locale: 'en', setLocale: () => {}, t: english => english });
const browserStorage = () => localStorage;
const initialLocale = () => readLocalePreference(browserStorage);

// Localize presentation only. API identifiers, user data and server messages
// must never be used as translation keys or rewritten by this provider.
export function LocaleProvider({ children }) {
  const [locale, updateLocale] = useState(initialLocale);
  function setLocale(next) {
    if (!isConsoleLocale(next)) return;
    updateLocale(next);
    publishLocalePreference(window, browserStorage, next);
  }
  useEffect(() => {
    const unsubscribe = subscribeLocalePreference(window, browserStorage, updateLocale);
    // Preserve initial default persistence and observe writes between render and mount.
    const current = initialLocale();
    updateLocale(current);
    writeLocalePreference(browserStorage, current);
    return unsubscribe;
  }, []);
  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);
  const t = (english, japanese) => locale === 'ja' ? japanese : english;
  return <LocaleContext.Provider value={{ locale, setLocale, t }}>
    <I18nProvider locale={locale} messages={[enMessages, jaMessages]}>{children}</I18nProvider>
  </LocaleContext.Provider>;
}

export function useLocale() { return useContext(LocaleContext); }
