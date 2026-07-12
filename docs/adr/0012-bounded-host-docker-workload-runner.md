# ADR-0012: host Docker daemon を bounded workload runner として使う

## Status

Accepted

## Context

digest 固定の Simulator image だけで cloud problem を起動する場合も、catalog が宣言する
OCI workload を materialize します。Simulator container の中に別の Docker
daemon を常駐させる方式は、追加の privileged process、別 image store、二重の cleanup を
持ち込みます。一方、host process だけを workload runner とすると、公開済み Simulator image
と同じ実行物を使うという ADR-0010 の境界が崩れます。

Docker socket は daemon と同等の強い権限を与えるため、一般の production service や
multi-tenant service に暗黙で渡してはいけません。ただし TenkaCloud local play は、既存の
Docker Challenge と同じ user-owned daemon 上で、review 済み catalog image だけを実行する
single-user development boundary です。

## Decision

TenkaCloud が所有する local launcher は、catalog に digest 固定 workload がある場合だけ
Simulator container に host daemon の UNIX socket を bind mount します。Simulator release
image には digest 固定した Docker CLI image から client binary だけを含めます。daemon は
同梱しません。

Simulator process は non-root user のまま動かし、launcher は socket の numeric group ID だけを
supplementary group として追加します。socket mount、group 追加、workload policy のいずれかを
構成できない場合、`Runtime::Workload/Materialize` を広告せず、該当 problem は resource 作成前の
capability preflight で失敗します。

runner が受け付けるのは versioned overlay から検証済みの image、command、unprivileged port、
health path だけです。image は digest pin と起動時 allowlist の両方を必須にし、read-only root、
non-root user、capability drop、`no-new-privileges`、CPU、memory、PID 上限、internal network を
固定します。overlay は host mount、Docker option、credential、secret を表現できません。

containerized Simulator は自身を world ごとの internal network に control-plane member として
接続し、health check と認証済み data-plane forwarding をその network 上で行います。participant
へ返す endpoint は別の fixed proxy container が host loopback にだけ publish します。world
cleanup は proxy、workload、network の順に削除し、Simulator 自身の network membership も
network 削除と同時に失われます。

この socket-enabled mode は TenkaCloud local play 専用です。hosted、shared、production deployment
で Docker socket を渡す構成は、この ADR の許可範囲に含めません。

## Consequences

- **Good**: 公開済み Simulator image と既存 Docker daemon だけで catalog workload を実行できる。
- **Good**: workload の host publish を loopback のまま維持し、container 内 health check も
  host-network の偶然に依存しない。
- **Bad**: Simulator process の侵害は local Docker daemon の侵害につながるため、image pin、
  allowlist、review 済み overlay、non-root 実行を同時に維持する必要がある。
- **Tradeoff**: rootless remote daemon や UNIX socket 以外の Docker endpoint は自動推測せず、
  明示対応を追加するまで workload capability を fail closed にする。

## References

- 関連コード: `Dockerfile`
- 関連コード: `tools/workload-runner/src/index.ts`
- 関連コード: `apps/server/src/runtime.ts`
- 関連 Issue: https://github.com/susumutomita/TenkaCloud/issues/2574
- 関連 ADR: [ADR-0010](./0010-pinned-multi-platform-oci-distribution.md)
