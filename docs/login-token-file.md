# Masked login input and explicit token-file fallback

This is a bounded alternative to typing/pasting into the existing masked login
field. It is not a diagnosis or confirmed repair of any Dia/password-manager
problem. Safari/Dia behavior, update version and matching URL/token must be
verified on the owner's actual device. No Mac, real profile, security setting,
extension or real user credential was accessed during development.

Choose **Choose token file** / **トークンファイルを選択** and explicitly select the
small token file supplied by the host. Nothing selects a path automatically.
The browser reads the selected file locally; the file and its name/path are not
uploaded. A successful parse only loads a masked value. Choose **Open sandbox**
/ **サンドボックスを開く** to ask the existing server to authenticate it.

The native server accepts 32–256 URL-safe characters (`A–Z`, `a–z`, digits, `_`,
`-`). Its CLI creates a64-character hex token followed by LF and bounds private
token files to512bytes. This UI checks size before reading (maximum512bytes),
decodes strict UTF-8 and accepts exactly one token with an optional single final
LF or CRLF. It does not trim spaces, extract labels/commands, interpret HTML,
accept BOM/multiple lines, or infer authority from a file's name. MIME may be
empty, `text/plain` or `application/octet-stream`; other MIME types are rejected,
and permitted metadata never replaces content validation. A well-formed but
wrong token still receives the existing401. Browser File APIs cannot establish
POSIX permissions; CLI0600 checks are not claimed for browser-selected files.

The file contents are not rendered as plaintext, logged, placed in browser
storage, or auto-submitted. The only credential transmission is the existing
same-origin form-encoded `token` sign-in POST; there is no multipart upload or
new server endpoint. Native `type=password`, session isolation and exact server
credential checks remain. Browser/password-manager security is not disabled.

Every file choice, typing and target paste starts a new token intent. A late read
or read error cannot overwrite a newer intent or clear its loading state. Empty
selection cancels a pending read while preserving the existing input. Invalid
new files clear the old value instead of making it look as if the invalid file
was accepted. Submit is disabled during reading; typing/pasting can replace that
intent. Unmount discards the pending result. Only fixed safe errors are shown.

English is the default via the shared locale contract. Explicit Japanese choice
persists only the non-secret locale preference. Language changes do not remount
Login, clear its token, restart file reading, submit credentials or change
identity. Error/status presentation follows the current language. No arbitrary
user content is passed as a translation key.

## Native input and password-manager hints

The field remains `type="password"`, with `autoComplete="off"` and per-field
`data-1p-ignore="true"` / `data-op-ignore="true"` hints. These hints do not disable
an extension, establish its behavior, or diagnose the cause of a failed paste.
There is no recommendation to disable browser protection as a permanent fix.

A connected native input may differ from React's controlled value after an
external/native write. Input capture and blur adopt that value as a new intent;
submission also reads the connected control, including an intentional empty
value rather than falling back to an older credential. A still-current explicit
paste takes precedence over another native write. If the control is disconnected,
submission falls back to controlled state. Adoption invalidates older file reads
and paste timers. These semantics do not guarantee preservation of an unobserved,
unfocused native write across every rerender: a locale render may reset such a
write before it is adopted. File parsing and server authentication remain separate.

Actual 1Password/Dia behavior, screenshot/image interpretation, OS clipboard,
and the new integrated binary's browser flows remain unverified. Existing source
and focused test evidence must not be presented as that device-level acceptance.

## Bounded input-intent model

[`LoginInputIntent.tla`](../formal/LoginInputIntent.tla) and its
[configuration](../formal/LoginInputIntent.cfg) describe committed input outcomes
using two opaque nonempty values and Empty, not real credentials. Sixteen scheduled
scenarios cover native insertion/clear, explicit paste precedence, disconnected
fallback, blur then locale, current/stale file completion, busy/file submit gates
and unmount. Revisions range from 0 to 3 without wrap, with at most six events and
two recorded POST attempts. These finite traces do not cover arbitrary event order.

The eight invariant families are TypeOK, NoEmptyPOST, NoPOSTWhileBlocked,
StaleFileCompletionFrame, SubmissionSelectionExact, AdoptedValueExact,
IntentInvalidatesAsync and LocaleIntentFrame. The transition functions model
selection, new-intent invalidation and publication; independent before-state
oracles check actual token/ref fields and POST records. The 12 mutation controls
change those transitions, leaving the oracles unchanged. The locale frame covers
adopted controlled intent, not unadopted DOM contents. The existing BrowserSessions
model covers its separate server-session boundary and does not prove input behavior.

At isolated model revision `28d37d1`, TLC explored 73 distinct states (89 generated,
queue empty, depth 7); all 12 controls produced their declared first invariant
failure in a noninitial state. The combined mutation named
UnmountedFileCompletionPublishes first failed the **file-edit** scenario: it does
not prove isolated alive-guard necessity or an unmount-specific counterexample.
The baseline includes the bounded unmount scenario. Model checking is not a
JavaScript/React/Cloudscape/DOM refinement proof, OS/extension test, file-parser
proof, cryptographic/authentication guarantee, liveness proof or full event-space
exploration. No real tokens are represented.

This login integration adds these 13 checks to the preserved 429-check inventory:
**49 models / 393 controls / 442 required executions**. The combined exact-HEAD
formal gate, new embedded-binary browser/E2E checks and hosted CI remain pending;
the scoped model result does not substitute for them.

## Input-scoped verification

`test/e2e/login_token_file.py` uses an actual chooser, owned dummy files and a real
local authenticated CLI. Optional `LOGIN_TEST_BUNDLE` injects only a private
Login-only JS/CSS bundle before integration; when used, its report explicitly
says this is an overlay, not the final embedded/full-console result. Authentication
and capability APIs are never mocked. TEST browser contexts have no permission
overrides; any clipboard seed permission belongs to a separate SETUP context.

The focused harness verifies: chooser/keyboard selection, LF/CRLF, real303/200 and
logout401/relogin, no auto-auth/upload,32/256 bounds without granting authority,
wrong-token401, MIME/BOM/binary/multiline/whitespace/oversize refusal, empty
selection, actual bounded read A finishing after B, typing and native paste
superseding an old read/error, current read error remaining visible, language
switch/persistence without credential persistence, navigation and PC1440/390
reflow/axe. Late-file fixtures delay an actual read promise; their timing is test
instrumentation, not a real-device diagnosis. Full clipboard regression, final
root-generated embedded assets, shared-provider navigation/language tests and
all immutable repository/CI gates remain integration obligations.
