/**
 * view テスト用の DOM 環境 setup。bunfig.toml の [test].preload
 * (repository root と apps/console の両方) から読み込まれる。
 *
 * preload である理由: Bun は CommonJS 依存 (react-dom や
 * @testing-library/dom) を module graph の link 時に先行実行する。
 * react-dom は load 時に document の有無 (canUseDOM) を判定して
 * onChange などの event system の経路を固定するため、テストファイル内の
 * import 順では登録が間に合わない。preload だけが全 module より先に
 * DOM を用意できる。
 *
 * happy-dom の GlobalRegistrator は DOM API に加えて fetch などの
 * ネットワーク・ストリーム実装も happy-dom のエミュレーションへ
 * 差し替える。このリポジトリの behavior テストは実 HTTP (Bun.serve) と
 * 実 SQLite を使う No Mock 方針なので、DOM API だけを happy-dom から
 * 借り、それ以外は Bun native の実装へ戻して通信経路を一切変えない。
 * FormData は React 19 の form action が DOM の form 要素から値を
 * 収集するのに必要なため happy-dom 実装のまま残す。
 */
import { GlobalRegistrator } from '@happy-dom/global-registrator';

declare global {
  // グローバル拡張の ambient 宣言は var で行う (TypeScript の仕様)。
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const bunNative = {
  AbortController: globalThis.AbortController,
  AbortSignal: globalThis.AbortSignal,
  Blob: globalThis.Blob,
  CloseEvent: globalThis.CloseEvent,
  ErrorEvent: globalThis.ErrorEvent,
  File: globalThis.File,
  Headers: globalThis.Headers,
  MessageChannel: globalThis.MessageChannel,
  MessageEvent: globalThis.MessageEvent,
  MessagePort: globalThis.MessagePort,
  ReadableStream: globalThis.ReadableStream,
  Request: globalThis.Request,
  Response: globalThis.Response,
  TextDecoder: globalThis.TextDecoder,
  TextEncoder: globalThis.TextEncoder,
  TransformStream: globalThis.TransformStream,
  URL: globalThis.URL,
  URLSearchParams: globalThis.URLSearchParams,
  WebSocket: globalThis.WebSocket,
  WritableStream: globalThis.WritableStream,
  fetch: globalThis.fetch,
  structuredClone: globalThis.structuredClone,
} as const;

GlobalRegistrator.register();

globalThis.AbortController = bunNative.AbortController;
globalThis.AbortSignal = bunNative.AbortSignal;
globalThis.Blob = bunNative.Blob;
globalThis.CloseEvent = bunNative.CloseEvent;
globalThis.ErrorEvent = bunNative.ErrorEvent;
globalThis.File = bunNative.File;
globalThis.Headers = bunNative.Headers;
globalThis.MessageChannel = bunNative.MessageChannel;
globalThis.MessageEvent = bunNative.MessageEvent;
globalThis.MessagePort = bunNative.MessagePort;
globalThis.ReadableStream = bunNative.ReadableStream;
globalThis.Request = bunNative.Request;
globalThis.Response = bunNative.Response;
globalThis.TextDecoder = bunNative.TextDecoder;
globalThis.TextEncoder = bunNative.TextEncoder;
globalThis.TransformStream = bunNative.TransformStream;
globalThis.URL = bunNative.URL;
globalThis.URLSearchParams = bunNative.URLSearchParams;
globalThis.WebSocket = bunNative.WebSocket;
globalThis.WritableStream = bunNative.WritableStream;
globalThis.fetch = bunNative.fetch;
globalThis.structuredClone = bunNative.structuredClone;

// React Testing Library の render / act を有効化する公式フラグ。
globalThis.IS_REACT_ACT_ENVIRONMENT = true;
