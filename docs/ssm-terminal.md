# Local participant guest sessions

The opt-in Battle runtime can expose a participant-scoped guest terminal when
`sessionImageSha256` is configured. The pin must equal the verified bundle's
selected BaseDisk SHA-256. The guest image must actually contain the private
relay: a pin or configuration flag is not a successful guest handshake. Default
native API mode does not start a VM or enable this transport.

A participant requests a terminal code from the Host's authenticated credentials
page. The Host uses the current job's participant viewer role, not the deploy
role or operator token, to sign a target-bound handoff. Copy the one-use code,
open the token-free Simulator terminal page, and paste the code into its form.
This is a manual handoff, not automatic sign-in. The form redeems by same-origin
POST and redirects to a clean console URL. Codes are not query parameters.

The resulting HttpOnly, SameSite=Strict cookie is separate from the existing
parameter-console cookie. It authenticates only this terminal's target, region
and original participant session. It cannot call generic AWS or identity APIs.
Role deletion, policy changes, session expiration and target replacement remain
subject to native authority checks. Sign out revokes the terminal cookie and
cancels its active channel consumers. Disconnect/sign-out does not claim guest
PTY termination; use Terminate for that operation. Uncertain cleanup remains
retained rather than silently replaced.

The browser explicitly starts or resumes a session. A one-time data-channel
token is sent in the first WebSocket frame, never in the URL. The server verifies
same-origin access and original participant identity before consuming it. Input,
resize and output use a bounded Simulator-specific JSON WebSocket protocol. This
is **not** compatible with the AWS `session-manager-plugin` or `ssmmessages`
binary wire protocol. The native session control operations and browser-specific
control route use the same session store and policy checks.

The browser displays output as inert text, not HTML or ANSI terminal commands.
Input is bounded to 8 KiB per message, resize to 300 rows by 500 columns, and
browser output retention to 1 MiB. Fragmented, unmasked or oversized WebSocket
messages are refused. Idle channels disconnect and require explicit resume; no
automatic Start retry can create another guest PTY after an uncertain response.
