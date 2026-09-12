package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

type ambiguousFieldError string

func (e ambiguousFieldError) Error() string {
	return fmt.Sprintf("ambiguous field: %s", string(e))
}

// Field returns the request field supplied with conflicting values.
func (e ambiguousFieldError) Field() string { return string(e) }

// decodeStrictJSON decodes exactly one JSON object. Unlike the standard
// decoder, it rejects trailing JSON values and duplicate top-level fields
// whose values are not semantically equal.
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

	fields := make(map[string]json.RawMessage)
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
		if previous, exists := fields[key]; exists {
			if !jsonValuesEqual(previous, value) {
				return ambiguousFieldError(key)
			}
			continue
		}
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
