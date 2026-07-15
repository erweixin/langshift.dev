package toolregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maximumJSONDepth = 64
	maximumJSONNodes = 100_000
)

func compileSchema(name string, encoded json.RawMessage) (*jsonschema.Schema, string, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil || validateSchemaRoot(document) != nil || validateJSONTree(document, true) != nil {
		return nil, "", ErrSchemaInvalid
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.AssertContent()
	location := "https://tool-registry.langshift.invalid/" + name + ".schema.json"
	if err = compiler.AddResource(location, document); err != nil {
		return nil, "", ErrSchemaInvalid
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return nil, "", errors.Join(ErrSchemaInvalid, err)
	}
	return compiled, hashBytes(canonicalSchemaBytes(encoded)), nil
}

func validateSchemaRoot(document any) error {
	root, ok := document.(map[string]any)
	if !ok || root["type"] != "object" {
		return ErrSchemaInvalid
	}
	closed := false
	if value, exists := root["additionalProperties"]; exists {
		closed = value == false
	}
	if value, exists := root["unevaluatedProperties"]; exists {
		closed = closed || value == false
	}
	if !closed {
		return ErrSchemaInvalid
	}
	return nil
}

func canonicalSchemaBytes(encoded json.RawMessage) json.RawMessage {
	canonical, err := canonicalJSON(encoded, 256<<10)
	if err != nil {
		return nil
	}
	return canonical
}

func canonicalJSON(encoded json.RawMessage, maximum int) (json.RawMessage, error) {
	if len(encoded) == 0 || len(encoded) > maximum {
		return nil, ErrSchemaInvalid
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > maximum {
		return nil, ErrSchemaInvalid
	}
	return canonical, nil
}

func canonicalInstance(encoded json.RawMessage, maximum int) (json.RawMessage, any, error) {
	if len(encoded) == 0 || len(encoded) > maximum {
		return nil, nil, ErrInputInvalid
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil || validateJSONTree(value, false) != nil {
		return nil, nil, ErrInputInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > maximum {
		return nil, nil, ErrInputInvalid
	}
	return canonical, value, nil
}

func validateJSONTree(root any, schema bool) error {
	type node struct {
		value any
		depth int
	}
	stack := []node{{root, 1}}
	seen := 0
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		seen++
		if seen > maximumJSONNodes || current.depth > maximumJSONDepth {
			return ErrSchemaInvalid
		}
		switch value := current.value.(type) {
		case map[string]any:
			for key, child := range value {
				if len(key) > 1024 {
					return ErrSchemaInvalid
				}
				if schema {
					switch key {
					case "$schema":
						text, ok := child.(string)
						if !ok || text != "https://json-schema.org/draft/2020-12/schema" {
							return ErrSchemaInvalid
						}
					case "$id":
						return ErrSchemaInvalid
					case "$ref", "$dynamicRef":
						text, ok := child.(string)
						if !ok || !strings.HasPrefix(text, "#") || strings.ContainsAny(text, "\x00\r\n") {
							return ErrSchemaInvalid
						}
					}
				}
				stack = append(stack, node{child, current.depth + 1})
			}
		case []any:
			for _, child := range value {
				stack = append(stack, node{child, current.depth + 1})
			}
		case string:
			if len(value) > 1<<20 {
				return ErrSchemaInvalid
			}
		case nil, bool, json.Number:
		default:
			return fmt.Errorf("%w: unsupported JSON value", ErrSchemaInvalid)
		}
	}
	return nil
}
