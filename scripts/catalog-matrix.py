"""Read a reviewed catalog snapshot; emit inventory, never execute catalog code.
Usage: python3 scripts/catalog-matrix.py CATALOG_ROOT [OUTPUT=docs/catalog.md]
"""
import json,subprocess,sys
from pathlib import Path
root=Path(sys.argv[1]).resolve();output=Path(sys.argv[2] if len(sys.argv)>2 else 'docs/catalog.md')
commit=subprocess.check_output(['git','-C',str(root),'rev-parse','HEAD'],text=True).strip()
rows=[];all_types=set()
for path in sorted(root.rglob('metadata.json')):
 if '.git' in path.parts:continue
 m=json.loads(path.read_text());runtime=m.get('runtime') or {};identifier=m['id'];types=set()
 templates=[m.get('cfnTemplate')]+[t.get('entry') for t in runtime.get('targets',[]) if t.get('engine')=='cloudformation']
 for template in filter(None,templates):
  source=(path.parent/template).resolve();assert source.is_relative_to(root)
  types.update(json.loads(subprocess.check_output(['node','tools/platform/catalog-types.mjs',str(source)],text=True))[str(source)])
 all_types.update(types)
 if runtime.get('provider')=='docker':status='不要（Docker実ワークロード）';need='Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可'
 elif runtime.get('provider')=='local' or identifier=='crypto-battle':status='不要（coordinationゲーム）';need='Bun/pluginの実行とゲーム固有状態。cryptoのIaCは任意付帯でありプレイ対応の根拠ではない'
 elif identifier=='hello-world':status='ローカル縦切り検証済み';need='Create/Describe/DeleteStack → SSM GetParameter。チーム別プロセス/DB/認証。実ホストflag採点100点・誤答5点減点・重複加点防止'
 else:status='未対応（選択不可）';need='実ワークロード、記載リソース/API・状態・IAMと固有採点が必要。APIだけの疑似成功では不十分'
 if runtime.get('kind')=='composite':need+='。GCP/Azure/Sakuraと各HTTPS probeも必要'
 rows.append((str(path.parent.relative_to(root)),identifier,status,', '.join(sorted(types)) or '—',(m.get('scoring') or {}).get('kind','coordination'),need))
text=f'''# 現行問題カタログ対応表\n\n入力: TenkaCloudChallenge `{commit}`。`scripts/catalog-matrix.py` がmetadataと参照テンプレートを読み取り生成。{len(rows)}問、Resources宣言のリソース型{len(all_types)}種類（AWS {sum(t.startswith('AWS::') for t in all_types)}、Custom {sum(t.startswith('Custom::') for t in all_types)}。MetadataのCloudFormation::Init等は含めない）。宣言上のIaC保有8問と、cloudワークロード7問（cryptoのcoordination主体を除く）は区別する。\n\nhello-worldだけを開発用ローカル接続で完走確認済み。TenkaCloud標準起動へ自動統合・配布済みという意味ではない。他の既存問題は削除しない。Docker20問とlocal coordination1問は本来の実行系で扱う。\n\n| 問題パス | Simulator要否・状態 | 宣言されたリソース型 | 採点 | 必要な状態・API/動作 |\n| --- | --- | --- | --- | --- |\n'''
for folder,identifier,status,types,score,need in rows:text+=f'| `{folder}` | {status} | {types} | {score} | {need} |\n'
text+='''\n## hello-worldの具体的対応\n\n| 段階 | 実行内容 | IAM/秘密・状態 | 画面/検証 |\n| --- | --- | --- | --- |\n| 取り込み | ホスト既存downloadCatalog、ハッシュ付きsealed snapshot | 問題コードを実行しない | hello-worldのみ選択可能 |\n| 起動 | 実テンプレートをAWS SDK CreateStackへ送信 | SSMとinline policyを同一SQLiteトランザクション作成。秘密Outputsはホストのみ | CREATE_COMPLETEと所有jobタグを実ホストが確認 |\n| 操作 | Systems Manager Parameter Store、GetParameter | ホスト発行15分tokenで固定identity。ssm読取のみ | 一覧→名前→Overview→Value。SDKも同じ値 |\n| 採点 | TenkaCloud既存HostingServiceのsubmit-flag | ホストのprivate outputと比較。採点ledgerはホストDB | 他チームflagはwrong、100/95点、8同時提出1加点 |\n| 分離/終了 | 2チーム別Go process/SQLite/token authority、DeleteStack | 他チームtoken 401、participant DescribeStacks 403。driftでも所有権保持 | DELETEDまで確認 |\n\n実STS、AWS federation/CloudShell/managed-policy権限・trust-policy評価は実装しない。接続用のローカルルーティングを本物の一時AWS資格情報として提示しない。テンプレートに含まれるAWS console URLはローカルアクセス先の根拠にしない。\n'''
output.parent.mkdir(parents=True,exist_ok=True);output.write_text(text)
print(f'{len(rows)} metadata; {len(all_types)} CloudFormation resource types; wrote {output}')
