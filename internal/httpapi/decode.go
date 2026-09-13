package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

type ambiguousFieldError string

func (e ambiguousFieldError) Error() string {
	return fmt.Sprintf("ambiguous field: %s", string(e))
}

// Field returns the request field supplied with conflicting values.
func (e ambiguousFieldError) Field() string { return string(e) }

// decodeStrictJSON decodes exactly one JSON object. Unlike the standard
// decoder, it rejects trailing JSON values and duplicate fields whose values
// are not semantically equal. Duplicate detection follows encoding/json's
// case-insensitive key matching: two spellings that bind to the same struct
// field (for example "source_rate" and "Source_Rate") count as the same
// field, so conflicting values under the two spellings are rejected.
func decodeStrictJSON(r io.Reader, dst any) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	firstToken, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := firstToken.(json.Delim)
	if !ok || delimiter != '{' {
		return fmt.Errorf("request body must be a JSON object")
	}

	knownFoldKeys := jsonFieldFoldKeys(dst)
	fields := make(map[string]json.RawMessage)
	// seenByFoldKey indexes supplied keys under their case-insensitive
	// spelling. encoding/json matches an object key to a struct field without
	// regard to case, so "source_rate" and "Source_Rate" populate the same
	// field; supplying both with different values leaves the meaning ambiguous.
	seenByFoldKey := make(map[string]string)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("JSON object key must be a string")
		}

		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return err
		}
		foldKey := strings.ToLower(key)
		if other, seen := seenByFoldKey[foldKey]; seen {
			exactRepeat := key == other
			// An exactly repeated key is always a duplicate. Two differently
			// cased spellings only collide when they unmarshal into the same
			// struct field; keys unknown to dst stay distinct and are ignored.
			if exactRepeat || knownFoldKeys[foldKey] {
				if !jsonValuesEqual(fields[other], value) {
					// Report the canonical field name for case variants.
					conflictKey := key
					if !exactRepeat {
						conflictKey = foldKey
					}
					return ambiguousFieldError(conflictKey)
				}
				if exactRepeat {
					continue
				}
			}
		}
		seenByFoldKey[foldKey] = key
		fields[key] = value
	}

	if _, err = decoder.Token(); err != nil {
		return err
	}
	objectEnd := decoder.InputOffset()
	if trailing := bytes.TrimLeft(raw[objectEnd:], " \t\r\n"); len(trailing) > 0 {
		return fmt.Errorf("request body must contain exactly one JSON value")
	}

	object := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		object[key] = value
	}
	rawObject, err := json.Marshal(object)
	if err != nil {
		return err
	}
	return json.Unmarshal(rawObject, dst)
}

func jsonValuesEqual(a, b json.RawMessage) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// jsonFieldFoldKeys returns the set of JSON object keys that unmarshal into a
// field of dst, keyed by their lower-cased spelling. It mirrors encoding/json's
// key resolution: a field carrying a json tag is matched by the tag name, an
// untagged field by its Go name, and matching is case-insensitive either way.
// Keys outside the set are unknown to the destination struct and ignored by
// unmarshalling, just like with the standard decoder.
func jsonFieldFoldKeys(dst any) map[string]bool {
	t := reflect.TypeOf(dst)
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	keys := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name := field.Name
		if tag, ok := field.Tag.Lookup("json"); ok {
			if name = strings.Split(tag, ",")[0]; name == "-" {
				continue
			}
		}
		keys[strings.ToLower(name)] = true
	}
	return keys
}
