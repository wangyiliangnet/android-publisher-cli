// Package strictjson rejects duplicate keys and encoding/json's case aliases.
package strictjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

func Decode(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := check(d, reflect.TypeOf(out).Elem()); err != nil {
		return fmt.Errorf("invalid JSON configuration")
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected one JSON document")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("invalid JSON configuration")
	}
	return nil
}
func check(d *json.Decoder, t reflect.Type) error {
	optional := t.Kind() == reflect.Pointer
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == nil {
		if optional {
			return nil
		}
		return fmt.Errorf("null is unsupported")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		if t.Kind() != reflect.Struct && t.Kind() != reflect.Map {
			return fmt.Errorf("unexpected object")
		}
		fields := map[string]reflect.Type{}
		if t.Kind() == reflect.Struct {
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				name := strings.Split(f.Tag.Get("json"), ",")[0]
				if name != "" && name != "-" {
					fields[name] = f.Type
				}
			}
		}
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			key, ok := k.(string)
			if e != nil || !ok || seen[key] {
				return fmt.Errorf("duplicate key")
			}
			seen[key] = true
			child := fields[key]
			if t.Kind() == reflect.Map {
				child = t.Elem()
			}
			if child == nil {
				return fmt.Errorf("unknown key")
			}
			if e = check(d, child); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid object")
		}
	case '[':
		if t.Kind() != reflect.Slice {
			return fmt.Errorf("unexpected array")
		}
		for d.More() {
			if err = check(d, t.Elem()); err != nil {
				return err
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid array")
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	return nil
}
