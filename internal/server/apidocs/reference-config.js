/* Full Scalar configuration: updateConfiguration replaces, rather than merges. */
(() => {
  'use strict';
  globalThis.SimulatorReferenceConfig=Object.freeze({
    create(previous,customFetch,locale){
      if(typeof customFetch!=='function'||!['en','ja'].includes(locale))throw Error('Invalid reference configuration');
      return {...(previous||{}),url:'/_simulator/docs/openapi.json',withDefaultFonts:false,
        theme:'default',darkMode:false,hideDarkModeToggle:true,hideClientButton:true,
        showDeveloperTools:'never',telemetry:false,agent:{disabled:true},proxyUrl:'',persistAuth:false,customFetch,
        localization:{...(previous?.localization||{}),locale,direction:'ltr',translations:SimulatorScalarLocale.translations(locale)}};
    }
  });
})();
