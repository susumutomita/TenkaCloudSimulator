package catalog

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func mlRows(t *testing.T, h *harness) int {
	t.Helper()
	var rows int
	if err := h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func guardrailInput(extra map[string]any) map[string]any {
	input := map[string]any{"name": "fixture", "blockedInputMessaging": "Blocked.", "blockedOutputsMessaging": "Blocked."}
	for k, v := range extra {
		input[k] = v
	}
	return input
}

// Guardrail policies are stored in their official read form, numbered
// versions do not exist and an update replaces the whole draft.
func TestBedrockGuardrailsProfilesAndLogging(t *testing.T) {
	h := newHarness(t, bedrock)
	key := "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000"
	refusals := []map[string]any{
		guardrailInput(map[string]any{"automatedReasoningPolicyConfig": map[string]any{"policies": []any{"arn:aws:bedrock:us-east-1:000000000000:automated-reasoning-policy/abc"}}}),
		guardrailInput(map[string]any{"kmsKeyId": "alias/guardrails"}),
	}
	for _, input := range refusals {
		expect(t, h.rest("bedrock", "POST", "/guardrails", input), 400, "ValidationException")
	}
	if mlRows(t, h) != 0 {
		t.Fatal("refused guardrails must not be stored")
	}
	created := h.rest("bedrock", "POST", "/guardrails", guardrailInput(map[string]any{"kmsKeyId": key, "description": "first",
		"topicPolicyConfig": map[string]any{"topicsConfig": []any{map[string]any{"name": "Investing", "definition": "Investment advice.", "type": "DENY"}}, "tierConfig": map[string]any{"tierName": "CLASSIC"}},
		"wordPolicyConfig":  map[string]any{"managedWordListsConfig": []any{map[string]any{"type": "PROFANITY"}}},
		"tags":              []any{map[string]any{"key": "team", "value": "a"}}}))
	expect(t, created, 202, "")
	id, arn := str(created.body["guardrailId"]), str(created.body["guardrailArn"])
	if len(id) != 12 || arn != "arn:aws:bedrock:us-east-1:000000000000:guardrail/"+id || created.body["version"] != "DRAFT" {
		t.Fatalf("guardrail identity %v", created.body)
	}
	got := h.rest("bedrock", "GET", "/guardrails/"+id+"?guardrailVersion=DRAFT", nil)
	expect(t, got, 200, "")
	topics := got.body["topicPolicy"].(map[string]any)
	if got.body["status"] != "READY" || got.body["kmsKeyArn"] != key || topics["topics"].([]any)[0].(map[string]any)["name"] != "Investing" || topics["tier"].(map[string]any)["tierName"] != "CLASSIC" {
		t.Fatalf("guardrail read form %v", got.body)
	}
	if got.body["wordPolicy"].(map[string]any)["managedWordLists"].([]any)[0].(map[string]any)["type"] != "PROFANITY" {
		t.Fatalf("word policy %v", got.body)
	}
	expect(t, h.rest("bedrock", "GET", "/guardrails/"+id+"?guardrailVersion=1", nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("bedrock", "DELETE", "/guardrails/"+id+"?guardrailVersion=1", nil), 404, "ResourceNotFoundException")
	if listed := h.rest("bedrock", "GET", "/guardrails?guardrailIdentifier="+url.QueryEscape(arn), nil).body["guardrails"].([]any); len(listed) != 1 || listed[0].(map[string]any)["id"] != id {
		t.Fatalf("guardrails by ARN %v", listed)
	}
	if listed := h.rest("bedrock", "GET", "/guardrails?guardrailIdentifier=other", nil).body["guardrails"].([]any); len(listed) != 0 {
		t.Fatalf("other guardrail %v", listed)
	}
	expect(t, h.rest("bedrock", "PUT", "/guardrails/"+id, guardrailInput(map[string]any{"crossRegionConfig": map[string]any{"guardrailProfileIdentifier": "us.guardrail.v1:0"}})), 400, "ValidationException")
	expect(t, h.rest("bedrock", "PUT", "/guardrails/"+id, guardrailInput(map[string]any{
		"contentPolicyConfig": map[string]any{"filtersConfig": []any{map[string]any{"type": "HATE", "inputStrength": "HIGH", "outputStrength": "HIGH"}}}})), 202, "")
	got = h.rest("bedrock", "GET", "/guardrails/"+url.PathEscape(arn), nil)
	if _, kept := got.body["topicPolicy"]; kept || got.body["description"] != nil || got.body["kmsKeyArn"] != nil || got.body["contentPolicy"].(map[string]any)["filters"].([]any)[0].(map[string]any)["type"] != "HATE" {
		t.Fatalf("update must replace the draft: %v", got.body)
	}

	model := "arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-3-haiku-20240307-v1:0"
	expect(t, h.rest("bedrock", "POST", "/inference-profiles", map[string]any{"inferenceProfileName": "copy", "modelSource": map[string]any{"copyFrom": "arn:aws:bedrock:us-east-1:000000000000:inference-profile/us.anthropic.claude-3-haiku-20240307-v1:0"}}), 400, "ValidationException")
	profile := h.rest("bedrock", "POST", "/inference-profiles", map[string]any{"inferenceProfileName": "app", "modelSource": map[string]any{"copyFrom": model}})
	expect(t, profile, 201, "")
	described := h.rest("bedrock", "GET", "/inference-profiles/"+url.PathEscape(str(profile.body["inferenceProfileArn"])), nil)
	if described.body["type"] != "APPLICATION" || described.body["models"].([]any)[0].(map[string]any)["modelArn"] != model {
		t.Fatalf("inference profile %v", described.body)
	}
	expect(t, h.rest("bedrock", "GET", "/inference-profiles", nil), 400, "ValidationException")
	if listed := h.rest("bedrock", "GET", "/inference-profiles?type=APPLICATION", nil).body["inferenceProfileSummaries"].([]any); len(listed) != 1 {
		t.Fatalf("application profiles %v", listed)
	}

	if got := h.rest("bedrock", "GET", "/logging/modelinvocations", nil); got.status != 200 || got.body["loggingConfig"] != nil {
		t.Fatalf("unset logging configuration %v", got.body)
	}
	expect(t, h.rest("bedrock", "DELETE", "/logging/modelinvocations", nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("bedrock", "PUT", "/logging/modelinvocations", map[string]any{"loggingConfig": map[string]any{"textDataDeliveryEnabled": true, "s3Config": map[string]any{"bucketName": "logs"}}}), 200, "")
	if got := h.rest("bedrock", "GET", "/logging/modelinvocations", nil); got.body["loggingConfig"].(map[string]any)["s3Config"].(map[string]any)["bucketName"] != "logs" {
		t.Fatalf("stored logging configuration %v", got.body)
	}
	expect(t, h.rest("bedrock", "DELETE", "/logging/modelinvocations", nil), 200, "")
	expect(t, h.rest("bedrock", "POST", "/model-invocation-job", map[string]any{}), 501, "UnknownOperationException")
}

// The default token vault is the only vault and needs a key ARN exactly
// for customer managed keys.
func TestBedrockAgentCoreIdentitiesAndTokenVault(t *testing.T) {
	h := newHarness(t, bedrockAgentCore)
	created := h.rest("bedrock-agentcore-control", "POST", "/identities/CreateWorkloadIdentity", map[string]any{"name": "agent-one", "tags": map[string]any{"team": "a"}})
	expect(t, created, 201, "")
	if created.body["workloadIdentityArn"] != "arn:aws:bedrock-agentcore:us-east-1:000000000000:workload-identity-directory/default/workload-identity/agent-one" {
		t.Fatalf("workload identity ARN %v", created.body)
	}
	expect(t, h.rest("bedrock-agentcore-control", "POST", "/identities/get-token-vault", map[string]any{"tokenVaultId": "other"}), 404, "ResourceNotFoundException")
	if got := h.rest("bedrock-agentcore-control", "POST", "/identities/get-token-vault", map[string]any{}); got.body["kmsConfiguration"].(map[string]any)["keyType"] != "ServiceManagedKey" {
		t.Fatalf("default token vault %v", got.body)
	}
	set := func(input map[string]any) reply {
		return h.rest("bedrock-agentcore-control", "POST", "/identities/set-token-vault-cmk", input)
	}
	expect(t, set(map[string]any{"tokenVaultId": "other", "kmsConfiguration": map[string]any{"keyType": "ServiceManagedKey"}}), 404, "ResourceNotFoundException")
	expect(t, set(map[string]any{"kmsConfiguration": map[string]any{"keyType": "CustomerManagedKey"}}), 400, "ValidationException")
	expect(t, set(map[string]any{"kmsConfiguration": map[string]any{"keyType": "ServiceManagedKey", "kmsKeyArn": "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000"}}), 400, "ValidationException")
	expect(t, set(map[string]any{"tokenVaultId": "default", "kmsConfiguration": map[string]any{"keyType": "CustomerManagedKey", "kmsKeyArn": "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000"}}), 200, "")
	if got := h.rest("bedrock-agentcore-control", "POST", "/identities/get-token-vault", map[string]any{"tokenVaultId": "default"}); got.body["kmsConfiguration"].(map[string]any)["keyType"] != "CustomerManagedKey" {
		t.Fatalf("stored token vault %v", got.body)
	}
}

// Flywheels start only from a task configuration because no trained
// Comprehend model exists.
func TestComprehendFlywheelsRefuseTrainedModels(t *testing.T) {
	h := newHarness(t, comprehend)
	base := func(extra map[string]any) map[string]any {
		input := map[string]any{"FlywheelName": "fixture", "DataAccessRoleArn": "arn:aws:iam::000000000000:role/comprehend", "DataLakeS3Uri": "s3://lake/flywheel"}
		for k, v := range extra {
			input[k] = v
		}
		return input
	}
	classification := map[string]any{"LanguageCode": "en", "DocumentClassificationConfig": map[string]any{"Mode": "MULTI_CLASS", "Labels": []any{"a", "b"}}}
	expect(t, h.json1("comprehend", "CreateFlywheel", base(map[string]any{"ActiveModelArn": "arn:aws:comprehend:us-east-1:000000000000:document-classifier/x"})), 400, "InvalidRequestException")
	expect(t, h.json1("comprehend", "CreateFlywheel", base(map[string]any{"TaskConfig": classification, "ModelType": "ENTITY_RECOGNIZER"})), 400, "InvalidRequestException")
	expect(t, h.json1("comprehend", "CreateFlywheel", base(nil)), 400, "InvalidRequestException")
	created := h.json1("comprehend", "CreateFlywheel", base(map[string]any{"TaskConfig": classification, "ModelType": "DOCUMENT_CLASSIFIER"}))
	expect(t, created, 200, "")
	arn := str(created.body["FlywheelArn"])
	expect(t, h.json1("comprehend", "UpdateFlywheel", map[string]any{"FlywheelArn": arn, "ActiveModelArn": "arn:aws:comprehend:us-east-1:000000000000:document-classifier/x"}), 400, "InvalidRequestException")
	expect(t, h.json1("comprehend", "UpdateFlywheel", map[string]any{"FlywheelArn": arn, "DataAccessRoleArn": "arn:aws:iam::000000000000:role/other"}), 200, "")
	properties := h.json1("comprehend", "DescribeFlywheel", map[string]any{"FlywheelArn": arn}).body["FlywheelProperties"].(map[string]any)
	if properties["Status"] != "ACTIVE" || properties["DataAccessRoleArn"] != "arn:aws:iam::000000000000:role/other" || properties["ActiveModelArn"] != nil {
		t.Fatalf("flywheel properties %v", properties)
	}
}

// Custom data sources take no connector configuration; connectors need it.
func TestKendraIndexesAndDataSources(t *testing.T) {
	h := newHarness(t, kendra)
	index := h.json1("kendra", "CreateIndex", map[string]any{"Name": "docs", "RoleArn": "arn:aws:iam::000000000000:role/kendra", "Edition": "DEVELOPER_EDITION"})
	expect(t, index, 200, "")
	id := str(index.body["Id"])
	source := func(input map[string]any) reply {
		input["IndexId"], input["Name"] = id, "source"
		return h.json1("kendra", "CreateDataSource", input)
	}
	s3 := map[string]any{"S3Configuration": map[string]any{"BucketName": "docs"}}
	expect(t, source(map[string]any{"Type": "CUSTOM", "RoleArn": "arn:aws:iam::000000000000:role/kendra"}), 400, "ValidationException")
	expect(t, source(map[string]any{"Type": "S3", "Configuration": s3}), 400, "ValidationException")
	expect(t, source(map[string]any{"Type": "CUSTOM"}), 200, "")
	connector := source(map[string]any{"Type": "S3", "Configuration": s3, "RoleArn": "arn:aws:iam::000000000000:role/kendra"})
	expect(t, connector, 200, "")
	described := h.json1("kendra", "DescribeIndex", map[string]any{"Id": id})
	if described.body["Edition"] != "DEVELOPER_EDITION" || described.body["Status"] != "ACTIVE" {
		t.Fatalf("index %v", described.body)
	}
	expect(t, h.json1("kendra", "DeleteIndex", map[string]any{"Id": id}), 200, "")
	expect(t, h.json1("kendra", "DescribeDataSource", map[string]any{"Id": connector.body["Id"], "IndexId": id}), 400, "ResourceNotFoundException")
	if mlRows(t, h) != 0 {
		t.Fatal("deleting an index deletes its data sources")
	}
}

// Bot networks are refused, updates replace the bot settings and only the
// DRAFT locale version exists.
func TestLexBotsAndDraftLocales(t *testing.T) {
	h := newHarness(t, lexModels)
	bot := map[string]any{"botName": "OrderFlowers", "roleArn": "arn:aws:iam::000000000000:role/lex-role", "dataPrivacy": map[string]any{"childDirected": false}, "idleSessionTTLInSeconds": 300}
	with := func(extra map[string]any) map[string]any {
		input := map[string]any{}
		for k, v := range bot {
			input[k] = v
		}
		for k, v := range extra {
			input[k] = v
		}
		return input
	}
	expect(t, h.rest("lexv2-models", "PUT", "/bots/", with(map[string]any{"botType": "BotNetwork"})), 400, "ValidationException")
	expect(t, h.rest("lexv2-models", "PUT", "/bots/", with(map[string]any{"botMembers": []any{map[string]any{"botMemberId": "ABCDEFGHIJ", "botMemberName": "x", "botMemberAliasId": "TSTALIASID", "botMemberAliasName": "x", "botMemberVersion": "1"}}})), 400, "ValidationException")
	created := h.rest("lexv2-models", "PUT", "/bots/", with(map[string]any{"description": "first"}))
	expect(t, created, 202, "")
	id := str(created.body["botId"])
	if len(id) != 10 || created.body["botStatus"] != "Available" {
		t.Fatalf("bot %v", created.body)
	}
	expect(t, h.rest("lexv2-models", "PUT", "/bots/"+id+"/", with(map[string]any{"botType": "BotNetwork"})), 400, "ValidationException")
	expect(t, h.rest("lexv2-models", "PUT", "/bots/"+id+"/", with(map[string]any{"idleSessionTTLInSeconds": 600})), 202, "")
	if got := h.rest("lexv2-models", "GET", "/bots/"+id+"/", nil); got.body["description"] != nil || asText(got.body["idleSessionTTLInSeconds"]) != "600" {
		t.Fatalf("update must replace bot settings: %v", got.body)
	}
	locales := "/bots/" + id + "/botversions/DRAFT/botlocales/"
	expect(t, h.rest("lexv2-models", "PUT", locales, map[string]any{"localeId": "english", "nluIntentConfidenceThreshold": 0.4}), 400, "ValidationException")
	expect(t, h.rest("lexv2-models", "PUT", locales, map[string]any{"localeId": "en_US", "nluIntentConfidenceThreshold": 0.4}), 202, "")
	if got := h.rest("lexv2-models", "GET", locales+"en_US/", nil); got.body["botLocaleStatus"] != "NotBuilt" || got.body["botVersion"] != "DRAFT" || got.body["botId"] != id {
		t.Fatalf("locale %v", got.body)
	}
	expect(t, h.rest("lexv2-models", "GET", "/bots/"+id+"/botversions/1/botlocales/en_US/", nil), 404, "ResourceNotFoundException")
	expect(t, h.rest("lexv2-models", "POST", "/bots/"+id+"/botversions/1/botlocales/", map[string]any{}), 400, "ValidationException")
	expect(t, h.rest("lexv2-models", "DELETE", "/bots/"+id+"/", nil), 202, "")
	if mlRows(t, h) != 0 {
		t.Fatal("deleting a bot deletes its locales")
	}
}

func lexicon(alphabet, language, lexemes string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><lexicon version="1.0" xmlns="http://www.w3.org/2005/01/pronunciation-lexicon" alphabet="` + alphabet + `" xml:lang="` + language + `">` + lexemes + `</lexicon>`
}

// PutLexicon applies the documented PLS rules with Polly's own exceptions.
func TestPollyLexiconsFollowPLSRules(t *testing.T) {
	h := newHarness(t, polly)
	good := "<lexeme><grapheme>W3C</grapheme><alias>World Wide Web Consortium</alias></lexeme>"
	refusals := map[string]string{
		lexicon("ipa", "en-US", strings.Repeat("<lexeme><grapheme>a</grapheme><alias>b</alias></lexeme>", 100)): "LexiconSizeExceededException",
		"not xml": "InvalidLexiconException",
		`<lexicon xmlns="urn:other" alphabet="ipa" xml:lang="en-US"></lexicon>`:                                             "InvalidLexiconException",
		lexicon("arpabet", "en-US", good):                                                                                   "UnsupportedPlsAlphabetException",
		lexicon("ipa", "xx-XX", good):                                                                                       "UnsupportedPlsLanguageException",
		lexicon("ipa", "en-US", "<lexeme><grapheme>W3C</grapheme></lexeme>"):                                                "InvalidLexiconException",
		lexicon("x-sampa", "en-US", "<lexeme><grapheme>W3C</grapheme><alias>"+strings.Repeat("w", 101)+"</alias></lexeme>"): "MaxLexemeLengthExceededException",
	}
	for content, code := range refusals {
		expect(t, h.rest("polly", "PUT", "/v1/lexicons/W3C", map[string]any{"Content": content}), 400, code)
	}
	expect(t, h.rest("polly", "PUT", "/v1/lexicons/W3C", map[string]any{"Content": lexicon("ipa", "en-US", good)}), 200, "")
	expect(t, h.rest("polly", "PUT", "/v1/lexicons/W3C", map[string]any{"Content": lexicon("x-sampa", "en-GB", good)}), 200, "")
	got := h.rest("polly", "GET", "/v1/lexicons/W3C", nil)
	if !strings.Contains(str(got.body["Lexicon"].(map[string]any)["Content"]), "en-GB") || got.body["LexiconAttributes"].(map[string]any)["LexiconArn"] != "arn:aws:polly:us-east-1:000000000000:lexicon/W3C" {
		t.Fatalf("lexicon %v", got.body)
	}
	if listed := h.rest("polly", "GET", "/v1/lexicons", nil).body["Lexicons"].([]any); len(listed) != 1 || listed[0].(map[string]any)["Attributes"] == nil {
		t.Fatalf("lexicons %v", listed)
	}
}

// Collections stay empty and AutoUpdate applies only to content moderation.
func TestRekognitionCollectionsAndProjects(t *testing.T) {
	h := newHarness(t, rekognition)
	created := h.json1("rekognition", "CreateCollection", map[string]any{"CollectionId": "faces", "Tags": map[string]any{"team": "a"}})
	expect(t, created, 200, "")
	if asText(created.body["StatusCode"]) != "200" || created.body["CollectionArn"] != "arn:aws:rekognition:us-east-1:000000000000:collection/faces" {
		t.Fatalf("collection %v", created.body)
	}
	if got := h.json1("rekognition", "DescribeCollection", map[string]any{"CollectionId": "faces"}); asText(got.body["FaceCount"]) != "0" || got.body["CollectionARN"] == nil {
		t.Fatalf("described collection %v", got.body)
	}
	expect(t, h.json1("rekognition", "CreateProject", map[string]any{"ProjectName": "labels", "AutoUpdate": "ENABLED"}), 400, "InvalidParameterException")
	project := h.json1("rekognition", "CreateProject", map[string]any{"ProjectName": "moderation", "Feature": "CONTENT_MODERATION", "AutoUpdate": "ENABLED"})
	expect(t, project, 200, "")
	if project.body["ProjectArn"] != "arn:aws:rekognition:us-east-1:000000000000:project/moderation/1" {
		t.Fatalf("project ARN %v", project.body)
	}
	if listed := h.json1("rekognition", "DescribeProjects", map[string]any{"ProjectNames": []any{"moderation"}}).body["ProjectDescriptions"].([]any); len(listed) != 1 || listed[0].(map[string]any)["Status"] != "CREATED" {
		t.Fatalf("projects %v", listed)
	}
	if deleted := h.json1("rekognition", "DeleteProject", map[string]any{"ProjectArn": project.body["ProjectArn"]}); deleted.body["Status"] != "DELETING" {
		t.Fatalf("deleted project %v", deleted.body)
	}
	expect(t, h.json1("rekognition", "ListCollections", map[string]any{}), 501, "UnknownOperationException")
}

// Vocabularies and filters take inline entries only.
func TestTranscribeInlineVocabularies(t *testing.T) {
	h := newHarness(t, transcribe)
	expect(t, h.json1("transcribe", "CreateVocabulary", map[string]any{"VocabularyName": "terms", "LanguageCode": "en-US", "VocabularyFileUri": "s3://bucket/terms.txt"}), 400, "BadRequestException")
	expect(t, h.json1("transcribe", "CreateVocabulary", map[string]any{"VocabularyName": "terms", "LanguageCode": "en-US"}), 400, "BadRequestException")
	expect(t, h.json1("transcribe", "CreateVocabulary", map[string]any{"VocabularyName": "terms", "LanguageCode": "en-US", "Phrases": []any{"TenkaCloud"}}), 200, "")
	expect(t, h.json1("transcribe", "UpdateVocabularyFilter", map[string]any{"VocabularyFilterName": "terms", "DataAccessRoleArn": "arn:aws:iam::000000000000:role/transcribe"}), 400, "BadRequestException")
	expect(t, h.json1("transcribe", "CreateVocabularyFilter", map[string]any{"VocabularyFilterName": "words", "LanguageCode": "en-US", "Words": []any{"darn"}}), 200, "")
	if got := h.json1("transcribe", "GetVocabulary", map[string]any{"VocabularyName": "terms"}); got.body["VocabularyState"] != "READY" || got.body["Phrases"] != nil {
		t.Fatalf("vocabulary %v", got.body)
	}
}

// Terminologies are derived from the inline CSV or TSV file.
func TestTranslateTerminologyFilesAndParallelData(t *testing.T) {
	h := newHarness(t, translate)
	file := func(text, format string, extra map[string]any) map[string]any {
		data := map[string]any{"File": base64.StdEncoding.EncodeToString([]byte(text)), "Format": format}
		for k, v := range extra {
			data[k] = v
		}
		return map[string]any{"Name": "terms", "MergeStrategy": "OVERWRITE", "TerminologyData": data}
	}
	refusals := []map[string]any{
		file("<tmx/>", "TMX", nil),
		file("en,ja\nA,B\n", "CSV", map[string]any{"Directionality": "MULTI"}),
		file("en,ja\n", "CSV", nil),
		file("English,Japanese\nA,B\n", "CSV", nil),
		file("en,ja\nA,B,C\n", "CSV", nil),
	}
	for _, input := range refusals {
		expect(t, h.json1("translate", "ImportTerminology", input), 400, "InvalidParameterValueException")
	}
	imported := h.json1("translate", "ImportTerminology", file("en,ja,fr\nTenkaCloud,天下クラウド,TenkaCloud\nsimulator,シミュレーター,simulateur\n", "CSV", nil))
	expect(t, imported, 200, "")
	properties := imported.body["TerminologyProperties"].(map[string]any)
	if properties["SourceLanguageCode"] != "en" || len(properties["TargetLanguageCodes"].([]any)) != 2 || asText(properties["TermCount"]) != "2" || properties["Arn"] != "arn:aws:translate:us-east-1:000000000000:terminology/terms/LATEST" {
		t.Fatalf("terminology %v", properties)
	}
	expect(t, h.json1("translate", "ImportTerminology", file("de\tes\nHallo\tHola\n", "TSV", nil)), 200, "")
	if got := h.json1("translate", "GetTerminology", map[string]any{"Name": "terms", "TerminologyDataFormat": "CSV"}).body["TerminologyProperties"].(map[string]any); got["SourceLanguageCode"] != "de" || got["Format"] != "TSV" {
		t.Fatalf("re-imported terminology %v", got)
	}
	expect(t, h.json1("translate", "CreateParallelData", map[string]any{"Name": "pairs", "ParallelDataConfig": map[string]any{"Format": "CSV"}, "ClientToken": "a"}), 400, "InvalidParameterValueException")
	created := h.json1("translate", "CreateParallelData", map[string]any{"Name": "pairs", "ParallelDataConfig": map[string]any{"S3Uri": "s3://bucket/pairs.csv", "Format": "CSV"}, "ClientToken": "a"})
	if created.status != 200 || created.body["Status"] != "CREATING" {
		t.Fatalf("parallel data %v", created.body)
	}
}

// Human review templates keep only their digest, flow definitions need an
// integration source for activation conditions and names stay lowercase.
func TestSageMakerHumanReviewAndMetadata(t *testing.T) {
	h := newHarness(t, sageMaker)
	expect(t, h.json1("sagemaker", "CreateHumanTaskUi", map[string]any{"HumanTaskUiName": "review", "UiTemplate": map[string]any{"Content": "<crowd-form></crowd-form>"}}), 200, "")
	ui := h.json1("sagemaker", "DescribeHumanTaskUi", map[string]any{"HumanTaskUiName": "review"})
	if len(str(ui.body["UiTemplate"].(map[string]any)["ContentSha256"])) != 64 || ui.body["HumanTaskUiStatus"] != "Active" {
		t.Fatalf("task UI %v", ui.body)
	}
	flow := map[string]any{"FlowDefinitionName": "review-flow", "RoleArn": "arn:aws:iam::000000000000:role/a2i",
		"OutputConfig":              map[string]any{"S3OutputPath": "s3://bucket/a2i"},
		"HumanLoopActivationConfig": map[string]any{"HumanLoopActivationConditionsConfig": map[string]any{"HumanLoopActivationConditions": "{}"}},
		"HumanLoopConfig": map[string]any{"WorkteamArn": "arn:aws:sagemaker:us-east-1:000000000000:workteam/private-crowd/team", "HumanTaskUiArn": ui.body["HumanTaskUiArn"],
			"TaskTitle": "Review", "TaskDescription": "Review the result.", "TaskCount": 1}}
	expect(t, h.json1("sagemaker", "CreateFlowDefinition", flow), 400, "ValidationException")
	flow["HumanLoopRequestSource"] = map[string]any{"AwsManagedHumanLoopRequestSource": "AWS/Textract/AnalyzeDocument/Forms/V1"}
	expect(t, h.json1("sagemaker", "CreateFlowDefinition", flow), 200, "")
	expect(t, h.json1("sagemaker", "CreateExperiment", map[string]any{"ExperimentName": "Upper"}), 400, "ValidationException")
	expect(t, h.json1("sagemaker", "CreateExperiment", map[string]any{"ExperimentName": "lower"}), 200, "")
	script := []any{map[string]any{"Content": base64.StdEncoding.EncodeToString([]byte("#!/bin/bash\necho start"))}}
	expect(t, h.json1("sagemaker", "CreateNotebookInstanceLifecycleConfig", map[string]any{"NotebookInstanceLifecycleConfigName": "Setup", "OnStart": script}), 400, "ValidationException")
	expect(t, h.json1("sagemaker", "CreateNotebookInstanceLifecycleConfig", map[string]any{"NotebookInstanceLifecycleConfigName": "setup", "OnStart": []any{map[string]any{"Content": "not base64!"}}}), 400, "ValidationException")
	expect(t, h.json1("sagemaker", "CreateNotebookInstanceLifecycleConfig", map[string]any{"NotebookInstanceLifecycleConfigName": "setup", "OnStart": script, "OnCreate": script}), 200, "")
	expect(t, h.json1("sagemaker", "CreateCodeRepository", map[string]any{"CodeRepositoryName": "Repo", "GitConfig": map[string]any{"RepositoryUrl": "https://example.com/repo.git"}}), 400, "ValidationException")
	expect(t, h.json1("sagemaker", "DescribeFlowDefinition", map[string]any{"FlowDefinitionName": "missing"}), 400, "ResourceNotFound")
}
