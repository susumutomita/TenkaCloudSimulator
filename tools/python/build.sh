#!/usr/bin/env bash
# Maintainer rebuild; development C toolchains are never runtime dependencies.
set -euo pipefail
repo_root=$(cd "$(dirname "$0")/../.." && pwd)
build_root=${PYTHON_BUILD_ROOT:-"$repo_root/.cache/python-wasi-build"}
mkdir -p "$build_root"
build_root=$(cd "$build_root" && pwd)
export SOURCE_DATE_EPOCH=1792952677
jobs=${BUILD_JOBS:-2}
clone_exact() {
  local name=$1 url=$2 ref=$3 expected=$4
  if [ ! -e "$build_root/$name" ]; then
    git clone --depth=1 --branch "$ref" "$url" "$build_root/$name"
  fi
  test "$(git -C "$build_root/$name" rev-parse HEAD)" = "$expected"
  test -z "$(git -C "$build_root/$name" status --porcelain --untracked-files=no)"
}
clone_exact cpython https://github.com/python/cpython.git v3.12.16 2c2462afc5d978d31f3d073001fc202d269917ff
clone_exact sqlite https://github.com/sqlite/sqlite.git version-3.54.1 57520227c95396ce39ba6ecaac8d475b78dd3964
clone_exact wasi-sdk-source https://github.com/WebAssembly/wasi-sdk.git wasi-sdk-34 5a0bf653a1a06e1c18867567c5937006d3394a69
if [ ! -e "$build_root/wasi-libc" ]; then
  git clone --no-checkout --depth=1 https://github.com/WebAssembly/wasi-libc.git "$build_root/wasi-libc"
  git -C "$build_root/wasi-libc" fetch --depth=1 origin 2e6fb9d8ee0cdf9e431fbcabe8af3115de000a13
  git -C "$build_root/wasi-libc" checkout --detach FETCH_HEAD
fi
test "$(git -C "$build_root/wasi-libc" rev-parse HEAD)" = 2e6fb9d8ee0cdf9e431fbcabe8af3115de000a13
test -z "$(git -C "$build_root/wasi-libc" status --porcelain --untracked-files=no)"
if [ ! -e "$build_root/wasi-sdk.tar.gz" ]; then
  curl --fail --location --silent --show-error https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-34/wasi-sdk-34.0-x86_64-linux.tar.gz -o "$build_root/wasi-sdk.tar.gz"
fi
printf '%s  %s\n' b761e3a0721dbae9c09a0059e5fdb2bf917d1b4a8a7b430fb3b5aafb0984b2c4 "$build_root/wasi-sdk.tar.gz" | sha256sum --check
if [ ! -e "$build_root/wasi-sdk-34.0-x86_64-linux" ]; then tar -xzf "$build_root/wasi-sdk.tar.gz" -C "$build_root"; fi
export WASI_SDK_PATH="$build_root/wasi-sdk-34.0-x86_64-linux"
mkdir -p "$build_root/llvm-notices" "$build_root/wasi-libs/include" "$build_root/wasi-libs/lib"
for path in LICENSE.TXT compiler-rt/LICENSE.TXT; do
  output=${path//\//-}
  curl --fail --location --silent --show-error "https://raw.githubusercontent.com/llvm/llvm-project/895aa2c896ada719451be2e3673c83da8ddf1141/$path" -o "$build_root/llvm-notices/$output"
done
python3 "$repo_root/tools/python/fetch-builtins.py" "$repo_root/tools/python/builtins.json" "$build_root/llvm-notices"
(
  cd "$build_root/sqlite"
  ./configure --disable-shared
  make -j"$jobs" sqlite3.c
  cp sqlite3.h "$build_root/wasi-libs/include/"
  "$WASI_SDK_PATH/bin/clang" --sysroot="$WASI_SDK_PATH/share/wasi-sysroot" -O2 -DSQLITE_THREADSAFE=0 -DSQLITE_OMIT_LOAD_EXTENSION -DSQLITE_OMIT_WAL -DSQLITE_OS_OTHER=0 -D_WASI_EMULATED_MMAN -D_WASI_EMULATED_SIGNAL -c sqlite3.c -o "$build_root/wasi-libs/lib/sqlite3.o"
  "$WASI_SDK_PATH/bin/llvm-ar" rcs "$build_root/wasi-libs/lib/libsqlite3.a" "$build_root/wasi-libs/lib/sqlite3.o"
)
mkdir -p "$build_root/cpython/builddir/build" "$build_root/cpython/builddir/wasi"
(
  cd "$build_root/cpython/builddir/build"
  ../../configure --without-ensurepip --disable-test-modules
  make -j"$jobs"
)
(
  cd "$build_root/cpython/builddir/wasi"
  # wazero 1.9 supports relative WASI clock subscriptions, not absolute ones.
  # Select CPython's standard nanosleep fallback; no interpreter source patch.
  ac_cv_func_clock_nanosleep=no ac_cv_lib_rt_clock_nanosleep=no CONFIG_SITE=../../Tools/wasm/config.site-wasm32-wasi LIBSQLITE3_CFLAGS="-I$build_root/wasi-libs/include" LIBSQLITE3_LIBS="-L$build_root/wasi-libs/lib -lsqlite3 -lwasi-emulated-mman -lwasi-emulated-signal" ../../Tools/wasm/wasi-env ../../configure --host=wasm32-unknown-wasi --build="$(../../config.guess)" --with-build-python="$build_root/cpython/builddir/build/python" --without-ensurepip --disable-test-modules --prefix=/python
  if grep -q "^#define HAVE_CLOCK_NANOSLEEP" pyconfig.h; then echo "Absolute clock sleep was unexpectedly enabled" >&2; exit 1; fi
  grep -q "^#define HAVE_NANOSLEEP 1" pyconfig.h
  touch ../../Modules/getbuildinfo.c
  ../../Tools/wasm/wasi-env make -j"$jobs" 'GITTAG=printf v3.12.16' 'GITBRANCH=printf tags/v3.12.16' 'GITVERSION=printf 2c2462afc5d978d31f3d073001fc202d269917ff'
  "$WASI_SDK_PATH/bin/llvm-strip" --strip-debug python.wasm
)
python3 "$repo_root/tools/python/package.py" --source "$build_root/cpython" --wasm "$build_root/cpython/builddir/wasi/python.wasm" --sqlite "$build_root/sqlite" --libc "$build_root/wasi-libc" --llvm "$build_root/llvm-notices" --sdk "$build_root/wasi-sdk-source"
