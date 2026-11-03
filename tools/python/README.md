# Embedded CPython / WASI

Go embeds assets from `internal/workload/python/assets`. No runtime download, host Python executable or external runtime service is used. Source, bootstrap, SDK HTTP and isolation tests establish actual execution; returning a predefined result based on a problem ID is prohibited.

Sources: CPython 3.12.16 `2c2462afc5d978d31f3d073001fc202d269917ff`, SQLite 3.54.1 `57520227c95396ce39ba6ecaac8d475b78dd3964`, WASI-SDK34 `5a0bf653a1a06e1c18867567c5937006d3394a69`. The SDK release's SHA256 is pinned in build.sh; libc/LLVM commits are recorded in third_party/python.json. Original rights bytes are embedded in PYTHON-NOTICES.txt with source/hash inventory. Comments and documentation acknowledgements are deliberately overinclusive; they do not mean every referenced optional module is linked.

```sh
# Maintainer development rebuild, Linux x64: gcc/make/tclsh/git/curl/Python3.
# Choose a dedicated build directory; existing source checkouts must match pins.
PYTHON_BUILD_ROOT=/path/to/dedicated/build BUILD_JOBS=2 bash tools/python/build.sh
python3 tools/python/check.py
```

The build uses the upstream WASI configuration, static SQLite (no load extension/WAL/threading), `/python` virtual prefix, no test extensions/ensurepip, fixed SOURCE_DATE_EPOCH, fixed Git version metadata and llvm-strip of debug information. Configure disables both direct and librt clock_nanosleep detection, selecting CPython's relative nanosleep path because wazero1.9 does not support absolute clock subscriptions. No interpreter source patch is applied. Original source license/comment bytes are preserved.

package.py writes a deterministic gzip/stdlib ZIP and records the built WASM identity. Cross-machine byte-for-byte reproducibility is not claimed merely from pinned inputs. check.py verifies the actual embedded artifact, each selected stdlib source and the complete original-notice inventory. CI runs this offline integrity gate plus real sandbox/SDK/browser tests; changing artifacts requires review and regenerated evidence. The source build has development toolchain requirements; `make build` and running the delivered Go executable do not invoke this script.

See [runtime limits and API/UI scope](../../docs/lambda.md). The runtime has no provider/problem/IAM/scoring logic; AWS binds the function definition and its authorization. All future service bridges must restrict calls to the stored role and explicit local resources, never expose a generic URL/host proxy.
