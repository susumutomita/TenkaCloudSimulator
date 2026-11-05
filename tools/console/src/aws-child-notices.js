// Translate only known static success notices at render time. Keep canonical
// state/in-flight results stable; never apply this to errors, resources or data.
export function localizeAwsNotice(message,t){
 switch(message){
  case "Role created. No service permissions have been granted.": return t("Role created. No service permissions have been granted.", "ロールを作成しました。サービスの許可はまだ付与されていません。");
  case "Policy saved.": return t("Policy saved.", "ポリシーを保存しました。");
  case "Inline policy deleted.": return t("Inline policy deleted.", "インラインポリシーを削除しました。");
  case "Role deleted.": return t("Role deleted.", "ロールを削除しました。");
  case "Role permissions saved.": return t("Role permissions saved.", "ロールの許可を保存しました。");
  case "Key created in this local region.": return t("Key created in this local region.", "このローカルリージョンにキーを作成しました。");
  case "Key enabled.": return t("Key enabled.", "キーを有効にしました。");
  case "Key disabled. Cryptographic operations are unavailable.": return t("Key disabled. Cryptographic operations are unavailable.", "キーを無効にしました。暗号操作は使用できません。");
  case "Function created.": return t("Function created.", "関数を作成しました。");
  case "Function code deployed.": return t("Function code deployed.", "関数コードをデプロイしました。");
  case "Function deleted.": return t("Function deleted.", "関数を削除しました。");
  case "REST API created.": return t("REST API created.", "REST API を作成しました。");
  case "Resource created.": return t("Resource created.", "リソースを作成しました。");
  case "Method and Lambda proxy integration created. An API-scoped Lambda invocation permission was added.": return t("Method and Lambda proxy integration created. An API-scoped Lambda invocation permission was added.", "メソッドと Lambda プロキシ統合を作成しました。この API に限定した Lambda 呼び出し許可を追加しました。");
  case "API deployed to a new stage.": return t("API deployed to a new stage.", "API を新しいステージにデプロイしました。");
  case "API deleted. Lambda functions and their resource policies remain separate resources.": return t("API deleted. Lambda functions and their resource policies remain separate resources.", "API を削除しました。Lambda 関数とそのリソースポリシーは別のリソースとして保持されます。");
  case "Changes saved.": return t("Changes saved.", "変更を保存しました。");
  case "Tags saved.": return t("Tags saved.", "タグを保存しました。");
  default: return message;
 }
}
