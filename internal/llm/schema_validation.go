package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	tekuri "github.com/santhosh-tekuri/jsonschema/v6"
)

type compiledSchemaResult struct {
	schema *tekuri.Schema
	err    error
}

var structuredSchemaCache sync.Map

func validateStructuredJSON(schemaJSON, payload json.RawMessage) error {
	compiled, err := compiledStructuredSchema(schemaJSON)
	if err != nil {
		return fmt.Errorf("compile structured output schema: %w", err)
	}
	value, err := decodeSingleJSON(payload)
	if err != nil {
		return fmt.Errorf("decode structured output: %w", err)
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("validate structured output: %w", err)
	}
	return nil
}

func compiledStructuredSchema(schemaJSON json.RawMessage) (*tekuri.Schema, error) {
	hash := sha256.Sum256(schemaJSON)
	cacheKey := hex.EncodeToString(hash[:])
	if cached, ok := structuredSchemaCache.Load(cacheKey); ok {
		result := cached.(compiledSchemaResult)
		return result.schema, result.err
	}

	value, err := decodeSingleJSON(schemaJSON)
	if err == nil {
		err = rejectNonLocalSchemaReferences(value)
	}
	var compiled *tekuri.Schema
	if err == nil {
		compiler := tekuri.NewCompiler()
		compiler.DefaultDraft(tekuri.Draft2020)
		compiler.UseLoader(denyRemoteSchemaLoader{})
		resourceURL := "https://schemas.tellbook.internal/" + cacheKey + ".json"
		if addErr := compiler.AddResource(resourceURL, value); addErr != nil {
			err = addErr
		} else {
			compiled, err = compiler.Compile(resourceURL)
		}
	}
	result := compiledSchemaResult{schema: compiled, err: err}
	actual, _ := structuredSchemaCache.LoadOrStore(cacheKey, result)
	stored := actual.(compiledSchemaResult)
	return stored.schema, stored.err
}

func decodeSingleJSON(payload []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func rejectNonLocalSchemaReferences(value any) error {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "$ref" || key == "$dynamicRef" {
				reference, ok := child.(string)
				if !ok || (reference != "#" && !strings.HasPrefix(reference, "#/")) {
					return fmt.Errorf("%s must be an in-document reference", key)
				}
			}
			if err := rejectNonLocalSchemaReferences(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range node {
			if err := rejectNonLocalSchemaReferences(child); err != nil {
				return err
			}
		}
	}
	return nil
}

type denyRemoteSchemaLoader struct{}

func (denyRemoteSchemaLoader) Load(resourceURL string) (any, error) {
	return nil, fmt.Errorf("remote schema loading is disabled for %q", resourceURL)
}
