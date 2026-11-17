package hashline

// RequestSchema returns the JSON Schema (an object schema, no $schema key) of a Request, for use as the input schema of
// an MCP tool. Every call builds a new map, so a tool can add its own properties, such as a path, to "properties" (a
// map[string]any) and to "required" (a []string) of the result.
//
// expected_hash is required: 64 lowercase hex digits, or "" to create a file. Exactly one of content and edits is
// required; edits has at least one item. No property outside the Request's is allowed, in the request or in an edit.
// An edit needs an op and a start anchor of the form line:hash; the per-op rules (content for replace and the inserts,
// none for delete, end only for replace and delete) are stated in the descriptions and enforced by [Apply].
//
// The "exactly one" rule is written with not and if/else at the top of the schema and not with oneOf, anyOf or allOf:
// the Anthropic API rejects a tool whose input schema has one of those at the top level, and keywords that count or
// name the properties would break when a tool adds its own.
func RequestSchema() map[string]any {
	anchorPattern := "^[1-9][0-9]*:[0-9a-f]{4}$"
	opDoc := "replace: the lines start through end become content. delete: remove the lines start through end. " +
		"insert_before / insert_after: add content before / after the line start."
	startDoc := "Anchor of the line (replace, delete: the first line of the range) as the read shows it, like 12:a3f1."
	endDoc := "Anchor of the last line of the range. Only for replace and delete; defaults to start."
	editContentDoc := "The new text. Required for replace, insert_before and insert_after, not allowed for delete. " +
		"Lines are separated by newlines; one newline at the very end is ignored and the file's own line endings are used. " +
		"An empty string is one blank line."
	hashDoc := "The hash of the file as you last read it. An empty string only creates a file that does not exist yet " +
		"and is refused when it exists."
	contentDoc := "The complete new content of the file, written exactly as given. Send either content or edits, not both."
	editsDoc := "Changes to the lines of the file as you last read it. Every anchor refers to that version, so an edit " +
		"never shifts the lines of another one. Edits must not overlap or insert at the same place; all are applied or " +
		"none. Send either edits or content, not both."

	editProps := map[string]any{}
	editProps["op"] = map[string]any{
		"type":        "string",
		"enum":        []string{OpReplace, OpDelete, OpInsertBefore, OpInsertAfter},
		"description": opDoc,
	}
	editProps["start"] = map[string]any{
		"type":        "string",
		"pattern":     anchorPattern,
		"description": startDoc,
	}
	editProps["end"] = map[string]any{
		"type":        "string",
		"pattern":     anchorPattern,
		"description": endDoc,
	}
	editProps["content"] = map[string]any{
		"type":        "string",
		"description": editContentDoc,
	}
	item := map[string]any{"type": "object", "properties": editProps, "required": []string{"op", "start"}, "additionalProperties": false}

	props := map[string]any{}
	props["expected_hash"] = map[string]any{
		"type":        "string",
		"pattern":     "^([0-9a-f]{64})?$",
		"description": hashDoc,
	}
	props["content"] = map[string]any{
		"type":        "string",
		"description": contentDoc,
	}
	props["edits"] = map[string]any{
		"type":        "array",
		"minItems":    1,
		"items":       item,
		"description": editsDoc,
	}

	s := map[string]any{"type": "object", "properties": props, "required": []string{"expected_hash"}, "additionalProperties": false}
	s["not"] = map[string]any{"required": []string{"content", "edits"}}
	s["if"] = map[string]any{"required": []string{"content"}}
	s["else"] = map[string]any{"required": []string{"edits"}}
	return s
}
