# Darwin clock basis, not runtime qualification

Darwin `New` reads CLOCK_UPTIME_RAW and couples the sample to Go's monotonic
`time.Time` component using the same bounded sampling interval as Linux.
The explicit bootstrap label is `darwin-clock-uptime-raw`, never
`linux-clock-monotonic`. Linux's constructor, label and signed wire format are
unchanged. Unknown internal clock kinds are rejected.

Evidence checked on the development Mac:
- Apple's installed `clock_gettime(3)` documents CLOCK_UPTIME_RAW as equivalent
  to mach_absolute_time after timebase conversion; it excludes system sleep.
  Darwin CLOCK_MONOTONIC includes sleep and is not a substitute.
- The installed Go 1.26.2 runtime's sys_darwin_arm64.s and sys_darwin_amd64.s
  nanotime trampolines call mach_absolute_time; sys_darwin.go converts the result.
  Source reference: https://go.googlesource.com/go/+/refs/tags/go1.26.2/src/runtime/
- The real Darwin unit test brackets the new clock against actual CLOCK_UPTIME_RAW
  samples and observes monotonic advancement. No wall-clock or mock is substituted.

This does not measure suspend/resume behavior experimentally or qualify Go 1.26.0
(the repository directive) by running that exact toolchain. The checked constructor
fails when sampling uncertainty exceeds its bound rather than widening the bound.

`BootstrapInherited` and `CloseInheritedExecutable` still reject Darwin. The Host
parser still rejects this label. The Battle unsupported-platform guards, inherited
FD3/FD4 contract and VM supervisor are unchanged. This package cannot make a Mac
Battle deployment work by itself. Future Host integration must preserve private
bootstrap, expiry/nonce/HMAC checks and domain identity; no clock label proves
that another machine shares a clock. No VM, launchd registration or denied log
operation is part of these tests.
