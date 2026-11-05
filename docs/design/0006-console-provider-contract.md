# Provider console and language contract

Implementation baseline: main `fcf8e702b45953cdeeba49883a238e638af725e8`.
This is work in progress, not a claim of real cloud console parity.

The global console chooses AWS, Google Cloud or Azure at the same level. Its
searchable catalog contains only that provider's implemented console services.
Existing service IDs, hash routes, request helper, authentication, capability
checks and native API identifiers remain stable. Unknown services are not
presented as implemented. Cloudscape AppLayout belongs to AWS only.

Google Cloud owns `gcp-console-shell.jsx`, `gcp-storage.jsx` and its CSS; Azure
owns `azure-console-shell.jsx`, `azure-blob.jsx` and its CSS. Shell exports are
default components with `{children, controls}`; `controls` is the integration
owner's neutral cloud/service/language/local-session controls. Each provider owns
its local scope, resource navigation and breadcrumbs. Views retain existing
`request`, `project` or `account` props; do not invent scope switching, IAM or
subscription permissions that the backend does not implement.

All components import `useLocale` from `./i18n.jsx`, returning
`{locale, setLocale, t}`. Use `t('English static copy', '日本語の表示文言')` for
presentation. Preserve canonical service names, resources, ARNs, API enums and
server error codes. English is the default; an explicit saved Japanese preference
persists locally. Only this non-secret preference is stored. Changing language
must not remount the console, reset a form, issue a new token or change authority.
Cloudscape's official English/Japanese component messages are bundled locally.

`login.jsx` owns the extracted login component and auth fallback. The initial extraction preserved the function body exactly; the subsequent
locale/token-file fallback retains the strict server sign-in transport and
clipboard revision behavior while adding explicitly tested UI state. Login remains masked, strict and
local. Neither provider shells nor language controls may inspect credentials.

Each writer uses a dedicated worktree. Main navigation is owned by
`cfn_adapters`, login by `grader_http`, GCP by `team_host`, Azure by
`waf_service`, AWS child modules by `cfn_callback`; root owns integration,
shared locale and generated assets. No concurrent generated asset edits.

Acceptance requires real clipboard/auth regression, language switching and
persistence without data loss, provider catalog filtering, desktop/mobile
operation, no horizontal overflow or external requests, original provider SDK
and console lifecycles, license retention and full repository gates. Official
public screenshots and documentation are references; authenticated current cloud
screens remain unverified and blocked asset fetches must not be retried.
