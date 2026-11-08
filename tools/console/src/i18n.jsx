import React, { createContext, useContext, useEffect, useState } from 'react';
import { I18nProvider } from '@cloudscape-design/components/i18n';
import enMessages from '@cloudscape-design/components/i18n/messages/all.en';
import jaMessages from '@cloudscape-design/components/i18n/messages/all.ja';

const preferenceKey = 'tenkacloud-simulator-locale';
const LocaleContext = createContext({ locale: 'en', setLocale: () => {}, t: english => english });
function initialLocale() {
  try { return localStorage.getItem(preferenceKey) === 'ja' ? 'ja' : 'en'; }
  catch { return 'en'; }
}

// Localize presentation only. API identifiers, user data and server messages
// must never be used as translation keys or rewritten by this provider.
export function LocaleProvider({ children }) {
  const [locale, updateLocale] = useState(initialLocale);
  function setLocale(next) { if (next === 'en' || next === 'ja') updateLocale(next); }
  useEffect(() => {
    document.documentElement.lang = locale;
    try { localStorage.setItem(preferenceKey, locale); } catch { /* Storage may be disabled. */ }
  }, [locale]);
  const t = (english, japanese) => locale === 'ja' ? japanese : english;
  return <LocaleContext.Provider value={{ locale, setLocale, t }}>
    <I18nProvider locale={locale} messages={[enMessages, jaMessages]}>{children}</I18nProvider>
  </LocaleContext.Provider>;
}

export function useLocale() { return useContext(LocaleContext); }
