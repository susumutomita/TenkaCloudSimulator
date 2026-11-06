// Original local illustration data; conceptual examples, never live inventory.
const data = {
  "base": "d166f569772de7f45d98e31e9971eeb33b903763",
  "providers": [
    {
      "id": "aws",
      "name": "AWS",
      "summary": "呼び出しと権限の関係をたどる",
      "defaultNode": "aws-lambda"
    },
    {
      "id": "gcp",
      "name": "Google Cloud",
      "summary": "バケットとオブジェクトを扱う",
      "defaultNode": "gcp-storage"
    },
    {
      "id": "azure",
      "name": "Azure",
      "summary": "コンテナーと Blob を扱う",
      "defaultNode": "azure-blob"
    }
  ],
  "nodes": [
    {
      "id": "aws-gateway",
      "provider": "aws",
      "title": "API Gateway",
      "subtitle": "リクエストの入口",
      "mark": "API",
      "x": 0.14,
      "y": 0.48,
      "description": "ローカル REST API のルートから、保存済みの Lambda 関数を呼び出す構成例です。",
      "limits": "REGIONAL / AWS_PROXY の限定的な実装です。公開ウェブサイトのホスティングや、実 AWS への接続は行いません。",
      "source": [
        "docs/apigateway.md",
        "internal/aws/apigateway_invoke.go"
      ]
    },
    {
      "id": "aws-lambda",
      "provider": "aws",
      "title": "Lambda",
      "subtitle": "Python をローカル実行",
      "mark": "λ",
      "x": 0.48,
      "y": 0.48,
      "description": "埋め込みの CPython / WASI で Python 関数を実行します。保存済みの実行ロールに基づき、限定された S3・SSM 操作を呼び出せます。",
      "limits": "任意の HTTP 通信やホストのファイルアクセスはできません。S3 への書き込みが関数を自動起動する構成ではありません。",
      "source": [
        "docs/lambda.md",
        "internal/aws/lambda_bridge.go"
      ]
    },
    {
      "id": "aws-s3",
      "provider": "aws",
      "title": "S3",
      "subtitle": "バケットとオブジェクト",
      "mark": "S3",
      "x": 0.82,
      "y": 0.24,
      "description": "バケットやオブジェクトを操作し、コンソールと API から同じローカルデータを確認できます。",
      "limits": "Lambda 内 SDK の対応範囲は GetObject・PutObject・DeleteObject です。図の線は関数からの明示的な呼び出し例です。",
      "source": [
        "docs/s3.md",
        "internal/aws/lambda_bridge.go"
      ]
    },
    {
      "id": "aws-ssm",
      "provider": "aws",
      "title": "Systems Manager",
      "subtitle": "Standard パラメータ",
      "mark": "SSM",
      "x": 0.82,
      "y": 0.72,
      "description": "Standard パラメータを保存し、関数から GetParameter・GetParameters で参照する流れを試せます。",
      "limits": "SecureString と実 KMS による暗号化は対応範囲外です。練習用の値だけを使用してください。",
      "source": [
        "internal/aws/ssm.go",
        "internal/aws/lambda_bridge.go"
      ]
    },
    {
      "id": "aws-iam",
      "provider": "aws",
      "title": "IAM",
      "subtitle": "実行ロールの権限",
      "mark": "IAM",
      "x": 0.48,
      "y": 0.12,
      "description": "保存済みの実行ロールとローカルポリシーが、関数からのサービス操作を制限します。",
      "limits": "実 AWS の STS・フェデレーションや、クラウド全体の IAM を再現するものではありません。",
      "source": [
        "docs/iam.md",
        "internal/aws/lambda_bridge.go"
      ]
    },
    {
      "id": "gcp-storage",
      "provider": "gcp",
      "title": "Cloud Storage",
      "subtitle": "ローカルのバケット",
      "mark": "GCS",
      "x": 0.25,
      "y": 0.48,
      "description": "ホストが設定した固定プロジェクトの範囲で、バケットとオブジェクトの限定的な JSON API を扱います。",
      "limits": "Google IAM・OAuth や任意のプロジェクト作成は再現しません。利用できる範囲はホストの設定によります。",
      "source": [
        "docs/multicloud-storage.md",
        "internal/server/cloud_storage.go"
      ]
    },
    {
      "id": "gcp-object",
      "provider": "gcp",
      "title": "Object",
      "subtitle": "内容と世代条件",
      "mark": "OBJ",
      "x": 0.75,
      "y": 0.48,
      "description": "オブジェクトの内容やメタデータを読み書きし、世代条件を使った操作を試せます。",
      "limits": "図はバケットとオブジェクトの所属関係です。AWS 側から自動転送される構成ではありません。",
      "source": [
        "docs/multicloud-storage.md",
        "internal/gcp/storage/http.go"
      ]
    },
    {
      "id": "azure-container",
      "provider": "azure",
      "title": "Container",
      "subtitle": "固定アカウントの保存先",
      "mark": "CTR",
      "x": 0.25,
      "y": 0.48,
      "description": "ホストが設定したローカルのストレージアカウントで、コンテナーと Blob を操作します。",
      "limits": "Azure RBAC・Entra・SAS やアカウント作成は再現しません。利用できる範囲はホストの設定によります。",
      "source": [
        "docs/multicloud-storage.md",
        "internal/server/cloud_storage.go"
      ]
    },
    {
      "id": "azure-blob",
      "provider": "azure",
      "title": "Blob Storage",
      "subtitle": "内容と ETag 条件",
      "mark": "BLB",
      "x": 0.75,
      "y": 0.48,
      "description": "BlockBlob のアップロードやダウンロード、ETag 条件を伴う操作をローカルで試せます。",
      "limits": "図はコンテナーと Blob の所属関係です。ステージングしたブロックや実クラウドの認証は対応範囲外です。",
      "source": [
        "docs/multicloud-storage.md",
        "internal/azure/blob/http.go"
      ]
    }
  ],
  "edges": [
    {
      "from": "aws-gateway",
      "to": "aws-lambda",
      "provider": "aws"
    },
    {
      "from": "aws-lambda",
      "to": "aws-s3",
      "provider": "aws"
    },
    {
      "from": "aws-lambda",
      "to": "aws-ssm",
      "provider": "aws"
    },
    {
      "from": "aws-iam",
      "to": "aws-lambda",
      "provider": "aws"
    },
    {
      "from": "gcp-storage",
      "to": "gcp-object",
      "provider": "gcp"
    },
    {
      "from": "azure-container",
      "to": "azure-blob",
      "provider": "azure"
    }
  ]
};
function freeze(value) {
  if (value && typeof value === "object") {
    Object.values(value).forEach(freeze);
    Object.freeze(value);
  }
  return value;
}
export const TOPOLOGY = freeze(data);
