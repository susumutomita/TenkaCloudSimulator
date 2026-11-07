// AWS settings presentation only. Locale persistence remains owned by LocaleProvider.
// This simulator supports English/Japanese, not AWS Unified Settings or browser-default locale.
export function awsLanguageSettings(locale, setLocale, t) {
  return {
    type:'menu-dropdown', title:t('Settings','設定'), ariaLabel:t('Settings','設定'),
    iconName:'settings', disableUtilityCollapse:true,
    items:[{id:'language', text:t('Language','言語'), expandable:true, items:[
      {id:'en', text:'English', itemType:'checkbox', checked:locale==='en'},
      {id:'ja', text:'日本語', itemType:'checkbox', checked:locale==='ja'},
    ]}],
    // Selecting the current locale keeps it selected; this is a single preference.
    // Presentation must remain usable during a native save, without replaying it.
    onItemClick:({detail}) => {
      if (detail.id==='en' || detail.id==='ja') setLocale(detail.id);
    },
  };
}
