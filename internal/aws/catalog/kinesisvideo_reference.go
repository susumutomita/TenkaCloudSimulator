package catalog

// PutMedia uses an HTTP body and newline ACK events. GetMedia uses a JSON
// selector and returns a Matroska byte stream. Both share the reviewed control
// service's signing name and GetDataEndpoint, but have distinct data-plane
// contracts outside the botocore kinesisvideo control-plane model.
func addKinesisVideoMediaReference(paths map[string]map[string]any, tag string) {
	stringField := map[string]any{"type": "string"}
	header := func(name, description string, required bool, schema map[string]any) map[string]any {
		return map[string]any{"in": "header", "name": name, "description": description, "required": required, "schema": schema}
	}
	routing := header("X-Simulator-Service", "Routes unsigned requests from this API reference.", true,
		map[string]any{"type": "string", "enum": []string{"kinesisvideo"}, "default": "kinesisvideo"})
	routing["example"] = "kinesisvideo"
	success := func(media string, schema map[string]any, description string) map[string]any {
		return map[string]any{"200": map[string]any{"description": description, "content": map[string]any{media: map[string]any{"schema": schema}}},
			"400": map[string]any{"description": "InvalidArgumentException or ClientLimitExceededException."},
			"401": map[string]any{"description": "NotAuthorizedException for the selected stream."},
			"404": map[string]any{"description": "ResourceNotFoundException."}}
	}
	discovery := paths["/getDataEndpoint"]["post"].(map[string]any)
	discovery["parameters"] = []any{routing}
	discovery["description"] = "Discover the local numeric-loopback origin for PUT_MEDIA or GET_MEDIA. Select exactly one stream name or ARN."
	discovery["responses"] = success("application/json", map[string]any{"type": "object", "required": []string{"DataEndpoint"}, "properties": map[string]any{"DataEndpoint": map[string]any{"type": "string", "format": "uri"}}}, "Local origin for the requested operation.")
	request := discovery["requestBody"].(map[string]any)
	request["required"] = true
	discoveryBody := request["content"].(map[string]any)["application/json"].(map[string]any)
	discoveryBody["example"] = map[string]string{"StreamName": "workshop-video", "APIName": "GET_MEDIA"}
	discoverySchema := discoveryBody["schema"].(map[string]any)
	discoverySchema["additionalProperties"] = false
	identity := []any{map[string]any{"required": []string{"StreamName"}}, map[string]any{"required": []string{"StreamARN"}}}
	discoverySchema["oneOf"] = identity
	discoverySchema["properties"].(map[string]any)["APIName"] = map[string]any{"type": "string", "enum": []string{"PUT_MEDIA", "GET_MEDIA"}}
	paths["/putMedia"] = map[string]any{"post": map[string]any{
		"tags": []string{tag}, "summary": "PutMedia", "operationId": "kinesisvideoPutMedia",
		"description":  "Send one finite Matroska segment with one AVC track. Supply exactly one stream header. Use the producer start time in seconds or RFC 3339.",
		"externalDocs": map[string]any{"url": "https://docs.aws.amazon.com/kinesisvideostreams/latest/APIReference/API_dataplane_PutMedia.html"},
		"parameters": []any{routing, header("x-amzn-stream-name", "Stream name.", false, stringField),
			header("x-amzn-stream-arn", "Stream ARN.", false, stringField),
			header("x-amzn-fragment-timecode-type", "The local media profile uses relative timestamps.", true, map[string]any{"type": "string", "enum": []string{"RELATIVE"}, "default": "RELATIVE"}),
			header("x-amzn-producer-start-timestamp", "Recording start timestamp.", true, stringField)},
		"requestBody": map[string]any{"required": true, "content": map[string]any{"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}},
		"responses":   success("application/json", stringField, "A stream of newline-delimited acknowledgement events. Retained media receives PERSISTED after commit."),
	}}
	selector := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"StartSelectorType"},
		"properties": map[string]any{"StartSelectorType": map[string]any{"type": "string", "enum": []string{"EARLIEST", "CONTINUATION_TOKEN"}}, "ContinuationToken": stringField},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"StartSelectorType": map[string]any{"const": "EARLIEST"}}, "not": map[string]any{"required": []string{"ContinuationToken"}}},
			map[string]any{"properties": map[string]any{"StartSelectorType": map[string]any{"const": "CONTINUATION_TOKEN"}}, "required": []string{"ContinuationToken"}},
		}}
	getSchema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"StartSelector"},
		"properties": map[string]any{"StreamARN": stringField, "StreamName": stringField, "StartSelector": selector},
		"oneOf":      identity}
	paths["/getMedia"] = map[string]any{"post": map[string]any{
		"tags": []string{tag}, "summary": "GetMedia", "operationId": "kinesisvideoGetMedia",
		"description":  "Read a finite snapshot of retained local media. Resume with the continuation token returned inside a previous fragment.",
		"externalDocs": map[string]any{"url": "https://docs.aws.amazon.com/kinesisvideostreams/latest/APIReference/API_dataplane_GetMedia.html"},
		"parameters":   []any{routing},
		"requestBody":  map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": getSchema, "example": map[string]any{"StreamName": "workshop-video", "StartSelector": map[string]any{"StartSelectorType": "EARLIEST"}}}}},
		"responses":    success("video/x-matroska", map[string]any{"type": "string", "format": "binary"}, "Actual stored Matroska segments and fragment metadata."),
	}}
}
