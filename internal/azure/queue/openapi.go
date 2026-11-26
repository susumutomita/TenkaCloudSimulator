package queue

import "strconv"

// OpenAPI is the local, independently rendered Queue Storage reference. The
// caller supplies its validated, host-configured account, never user input.
// A server can serve this under its existing same-origin Scalar session.
func OpenAPI(account string) map[string]any {
	const base = "/_simulator/providers/azure-queue"
	const queues = "/{account}/{queue}"
	const messages = queues + "/messages"
	const message = messages + "/{messageId}"
	const doc = "https://learn.microsoft.com/en-us/rest/api/storageservices/queue-service-rest-api"
	const description = "Bounded Azure Queue Storage REST operations for this local account, not Microsoft Azure. Sign in to the simulator first; Scalar Test Request uses the same-origin session cookie. SDKs and curl use the optional X-Simulator-Token header in place of that cookie. Only the configured local owner is authorized. Do not enter Azure credentials. Examples are static; send a request to inspect live state. Queue listing, metadata, ARM provisioning and Azure identities are not supported."

	type obj = map[string]any
	parameter := func(name, in, description string, required bool, schema obj) obj {
		return obj{"name": name, "in": in, "description": description, "required": required, "schema": schema}
	}
	integer := func(minimum, maximum int) obj {
		return obj{"type": "integer", "minimum": minimum, "maximum": maximum}
	}
	stringSchema := func() obj { return obj{"type": "string"} }
	ref := func(name string) obj { return obj{"$ref": "#/components/schemas/" + name} }
	xmlContent := func(schema obj) obj { return obj{"application/xml": obj{"schema": schema}} }
	response := func(description string) obj { return obj{"description": description} }
	xmlResponse := func(description string, schema obj) obj {
		return obj{"description": description, "content": xmlContent(schema)}
	}
	body := func(required bool) obj {
		return obj{"required": required, "content": obj{"application/xml": obj{
			"schema": ref("MessageInput"), "example": "<QueueMessage><MessageText>local example</MessageText></QueueMessage>",
		}}}
	}
	version := parameter("x-ms-version", "header", "Only this version is supported.", true, obj{"type": "string", "enum": []string{apiVersion}, "default": apiVersion})
	token := parameter("X-Simulator-Token", "header", "Host-issued local token for clients without a console session. Not an Azure credential.", false, obj{"type": "string", "writeOnly": true})
	scope := parameter("account", "path", "Fixed host-configured storage account.", true, obj{"type": "string", "enum": []string{account}, "default": account})
	name := parameter("queue", "path", "Queue name: 3 to 63 lowercase letters, digits or hyphens; no consecutive hyphens.", true,
		obj{"type": "string", "pattern": queueName.String(), "not": obj{"pattern": "--"}, "example": "example-queue"})
	id := parameter("messageId", "path", "Message UUID returned by the service.", true, obj{"type": "string", "format": "uuid"})
	popReceipt := parameter("popreceipt", "query", "Current opaque receipt for the message. Send the replacement receipt after a receive or update; do not reuse the previous one.", true, obj{"type": "string", "minLength": 1})

	operation := func(id, summary, detail string, success obj, specific ...obj) obj {
		parameters := append([]obj{scope, name, version, token}, specific...)
		for status, text := range map[string]string{
			"400": "Invalid request (for example, API version, queue name, XML, query parameter or pop receipt).",
			"401": "Valid local session or token is required.",
			"403": "Authenticated local subject does not own this account.",
			"404": "Account, queue or message does not exist.",
			"413": "Request body exceeds " + strconv.Itoa(maxRequest) + " bytes.",
			"500": "Local storage operation failed.",
			"501": "Operation or request option is outside the supported subset.",
		} {
			// Errors from the Queue leaf are native XML. A request refused
			// upstream can have a different response envelope.
			if status == "401" || status == "403" {
				success[status] = response(text)
				continue
			}
			success[status] = xmlResponse(text, ref("QueueError"))
		}
		return obj{"operationId": id, "summary": summary, "description": detail, "parameters": parameters, "responses": success}
	}

	create := operation("CreateQueue", "Create queue", "Create the named queue in the local account. Calling again leaves the existing queue unchanged.", obj{
		"201": response("Created."), "204": response("Already exists."), "409": xmlResponse("Local queue count quota exceeded.", ref("QueueError")),
	})
	send := operation("PutMessage", "Send message", "Enqueue UTF-8 message content; visibility delay must be shorter than its lifetime.", obj{
		"201": xmlResponse("Message accepted; identity and initial receipt returned.", ref("SentMessages")),
		"409": xmlResponse("Local account message count or storage quota exceeded.", ref("QueueError")),
	}, parameter("messagettl", "query", "Default 604800 seconds; bounded local values are 1 to 604800, or -1 for no expiry.", false,
		obj{"type": "integer", "anyOf": []obj{{"const": -1}, {"minimum": 1, "maximum": week}}}),
		parameter("visibilitytimeout", "query", "Seconds before the message can be seen. Default 0.", false, integer(0, week)))
	send["requestBody"] = body(true)
	get := operation("GetOrPeekMessages", "Receive or peek messages", "Leave peekonly unset to receive and reserve messages. To peek without incrementing the dequeue count or changing visibility, send peekonly=true and omit visibilitytimeout. Peek does not return a pop receipt or next visibility time.", obj{
		"200": xmlResponse("Visible messages, or an empty QueueMessagesList.", obj{"anyOf": []obj{ref("ReceivedMessages"), ref("PeekedMessages")}}),
	}, parameter("peekonly", "query", "Set true to peek; omit to receive.", false, obj{"type": "boolean", "enum": []bool{true}}),
		parameter("numofmessages", "query", "Maximum number of messages. Default 1.", false, integer(1, 32)),
		parameter("visibilitytimeout", "query", "Reserve for this many seconds on receive (default 30). Not valid with peekonly=true.", false, integer(1, week)))
	update := operation("UpdateMessage", "Update message", "Use the message ID and its current receipt. Leave the body empty to change only visibility; with an XML body, replace the message text as well. The new deadline cannot exceed the message expiry.", obj{
		"204": obj{"description": "Updated. Replace the old pop receipt with the response header.", "headers": obj{
			"x-ms-popreceipt":        obj{"description": "Replacement opaque receipt.", "schema": stringSchema()},
			"x-ms-time-next-visible": obj{"description": "UTC next visibility time in RFC 1123 format.", "schema": stringSchema()},
		}},
		"409": xmlResponse("Local account storage quota exceeded.", ref("QueueError")),
	}, id, popReceipt, parameter("visibilitytimeout", "query", "Seconds before the updated message becomes visible.", true, integer(0, week)))
	update["requestBody"] = body(false)

	listSchema := func(receipt, payload bool) obj {
		datetime := obj{"type": "string", "description": "UTC HTTP-date (RFC 1123)."}
		properties := obj{
			"MessageId": obj{"type": "string", "format": "uuid"}, "InsertionTime": datetime, "ExpirationTime": datetime,
		}
		required := []string{"MessageId", "InsertionTime", "ExpirationTime"}
		if receipt {
			properties["PopReceipt"], properties["TimeNextVisible"] = stringSchema(), datetime
			required = append(required, "PopReceipt", "TimeNextVisible")
		}
		if payload {
			properties["DequeueCount"], properties["MessageText"] = obj{"type": "integer", "minimum": 0}, stringSchema()
			required = append(required, "DequeueCount", "MessageText")
		}
		return obj{"type": "object", "xml": obj{"name": "QueueMessagesList"}, "properties": obj{
			"QueueMessage": obj{"type": "array", "items": obj{"type": "object", "properties": properties, "required": required}},
		}}
	}

	return obj{
		"openapi": "3.1.0", "info": obj{"title": "Azure Queue Storage (local simulator)", "version": apiVersion, "description": description},
		"externalDocs": obj{"description": "Microsoft Queue Storage REST API", "url": doc},
		"servers":      []obj{{"url": base, "description": "This simulator only"}},
		"paths": obj{
			queues: obj{
				"put":    create,
				"delete": operation("DeleteQueue", "Delete queue", "Delete the named queue and all of its messages. This is permanent.", obj{"204": response("Deleted.")}),
			},
			messages: obj{
				"get": get, "post": send,
				"delete": operation("ClearMessages", "Clear messages", "Delete all visible and reserved messages in the named queue without deleting the queue.", obj{"204": response("Cleared.")}),
			},
			message: obj{
				"put":    update,
				"delete": operation("DeleteMessage", "Delete message", "Delete using the current receipt, not an old receipt from a previous receive or update.", obj{"204": response("Deleted.")}, id, popReceipt),
			},
		},
		"components": obj{"schemas": obj{
			"MessageInput": obj{"type": "object", "xml": obj{"name": "QueueMessage"}, "required": []string{"MessageText"}, "additionalProperties": false,
				"properties": obj{"MessageText": obj{"type": "string", "description": "UTF-8 XML text, at most " + strconv.Itoa(maxMessage) + " bytes."}}},
			"SentMessages": listSchema(true, false), "ReceivedMessages": listSchema(true, true), "PeekedMessages": listSchema(false, true),
			"QueueError": obj{"type": "object", "xml": obj{"name": "Error"}, "required": []string{"Code", "Message"},
				"properties": obj{"Code": stringSchema(), "Message": stringSchema()}},
		}},
	}
}
