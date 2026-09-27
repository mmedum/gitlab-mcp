package gapi

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Drift is GitLab adding a response field. The wire types here model a
// subset on purpose (§4.10), so an unknown field is routine and must not
// fail the call; it is reported by path at debug level, once per
// process, so a field worth modeling is noticed. A field the struct
// declares and GitLab stops sending surfaces in `scripts/gates
// api-fields` instead.

// decodeReporting unmarshals data into out and calls drift once for each
// field of the response out does not model.
func decodeReporting(data []byte, out any, drift func(path string)) error {
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil //nolint:nilerr // the typed decode succeeded; drift is advisory
	}
	t := reflect.TypeOf(out)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	reportUnknown(typeName(t), t, raw, drift)
	return nil
}

func typeName(t reflect.Type) string {
	for t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Pointer) {
		t = t.Elem()
	}
	if t == nil || t.Name() == "" {
		return "response"
	}
	return t.Name()
}

func reportUnknown(path string, t reflect.Type, value any, drift func(string)) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := value.(map[string]any)
		if !ok {
			return
		}
		fields := jsonFields(t)
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ft, known := fields[k]
			if !known {
				drift(path + "." + k)
				continue
			}
			reportUnknown(path+"."+k, ft, obj[k], drift)
		}
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return
		}
		for _, item := range items {
			reportUnknown(path, t.Elem(), item, drift)
		}
	case reflect.Map:
		obj, ok := value.(map[string]any)
		if !ok {
			return
		}
		// A map models an open set of keys; only its values are walked.
		for _, v := range obj {
			reportUnknown(path, t.Elem(), v, drift)
		}
	default:
		// A scalar, or an any-typed field, models nothing to check.
	}
}

var fieldCache sync.Map

// jsonFields maps a struct's wire names to their types, following
// embedded structs the way encoding/json does.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	if cached, ok := fieldCache.Load(t); ok {
		return cached.(map[string]reflect.Type)
	}
	out := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				for k, v := range jsonFields(et) {
					out[k] = v
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	fieldCache.Store(t, out)
	return out
}
