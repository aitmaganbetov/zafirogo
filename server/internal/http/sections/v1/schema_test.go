package v1_test

// Проверка JSON-ответов по схемам OpenAPI (минимальный валидатор: $ref, allOf, object, array, string, integer,
// number, boolean, nullable, enum). Контракт v1 сверяется и с api/v1.yaml, и со старой спецификацией
// resources/api/openapi-v1.yaml (testdata/openapi-v1.legacy.yaml): ответы нового сервера должны проходить обе.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

type openAPI struct {
	name string
	root map[string]any
}

func loadSpec(t testing.TB, name, path string) *openAPI {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(raw, &root); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return &openAPI{name: name, root: root}
}

func m(v any) map[string]any { x, _ := v.(map[string]any); return x }

func (s *openAPI) resolve(v any) map[string]any {
	node := m(v)
	for node != nil {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		cur := any(s.root)
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cur = m(cur)[part]
		}
		node = m(cur)
	}
	return node
}

// operations — "МЕТОД /путь" всех операций спецификации.
func (s *openAPI) operations() []string {
	var out []string
	for p, item := range m(s.root["paths"]) {
		for method := range m(item) {
			switch method {
			case "get", "post", "put", "patch", "delete":
				out = append(out, strings.ToUpper(method)+" "+p)
			}
		}
	}
	slices.Sort(out)
	return out
}

// responseSchema — схема ответа операции; nil — ответ с этим статусом не описан.
func (s *openAPI) responseSchema(method, path string, status int) map[string]any {
	op := m(m(m(s.root["paths"])[path])[strings.ToLower(method)])
	if op == nil {
		return nil
	}
	resp := s.resolve(m(op["responses"])[strconv.Itoa(status)])
	if resp == nil {
		return nil
	}
	return s.resolve(m(m(m(resp["content"])["application/json"])["schema"]))
}

// errorSchema — components.schemas.Error.
func (s *openAPI) errorSchema() map[string]any {
	return s.resolve(m(m(s.root["components"])["schemas"])["Error"])
}

// validate — ошибки значения v по схеме sch. strict — все объявленные свойства объекта обязательны и лишних нет
// (представления Transformers всегда отдают все поля).
func (s *openAPI) validate(sch map[string]any, v any, at string, strict bool) []string {
	sch = s.resolve(sch)
	if sch == nil {
		return nil
	}
	var errs []string
	if all, ok := sch["allOf"].([]any); ok {
		props := map[string]any{}
		for _, part := range all {
			p := s.resolve(part)
			errs = append(errs, s.validate(p, v, at, false)...)
			for k, pv := range m(p["properties"]) {
				props[k] = pv
			}
		}
		if strict {
			errs = append(errs, extraKeys(props, v, at)...)
		}
		return errs
	}
	if v == nil {
		if nullable, _ := sch["nullable"].(bool); nullable || sch["type"] == nil {
			return nil
		}
		return []string{at + ": null"}
	}
	if enum, ok := sch["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s: %v not in enum", at, v))
		}
	}
	switch sch["type"] {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return append(errs, at+": not an object")
		}
		props := m(sch["properties"])
		for k, ps := range props {
			val, present := obj[k]
			if !present {
				if strict {
					errs = append(errs, at+"."+k+": missing")
				}
				continue
			}
			errs = append(errs, s.validate(m(ps), val, at+"."+k, strict)...)
		}
		if req, ok := sch["required"].([]any); ok {
			for _, r := range req {
				if _, present := obj[fmt.Sprint(r)]; !present {
					errs = append(errs, at+"."+fmt.Sprint(r)+": required")
				}
			}
		}
		if ap := m(sch["additionalProperties"]); ap != nil {
			for k, val := range obj {
				if _, declared := props[k]; !declared {
					errs = append(errs, s.validate(ap, val, at+"."+k, strict)...)
				}
			}
		} else if strict && props != nil {
			errs = append(errs, extraKeys(props, v, at)...)
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return append(errs, at+": not an array")
		}
		for i, e := range arr {
			errs = append(errs, s.validate(m(sch["items"]), e, fmt.Sprintf("%s[%d]", at, i), strict)...)
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			return append(errs, at+": not a string")
		}
		if sch["format"] == "date-time" {
			if _, err := time.Parse(time.RFC3339, str); err != nil {
				errs = append(errs, at+": not date-time: "+str)
			}
		}
	case "integer":
		n, ok := v.(json.Number)
		if _, err := n.Int64(); !ok || err != nil {
			errs = append(errs, fmt.Sprintf("%s: not an integer: %v", at, v))
		}
	case "number":
		if _, ok := v.(json.Number); !ok {
			errs = append(errs, at+": not a number")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			errs = append(errs, at+": not a boolean")
		}
	}
	return errs
}

func extraKeys(props map[string]any, v any, at string) []string {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	var errs []string
	for k := range obj {
		if _, declared := props[k]; !declared {
			errs = append(errs, at+"."+k+": not in spec")
		}
	}
	return errs
}

func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

// checkContract — ответ (status, body) операции method path (путь спецификации) соответствует схеме. Ошибки
// (4xx/5xx) — схеме Error, даже если статус у операции не описан (как и в старом API: конверт один).
func (s *openAPI) checkContract(t testing.TB, method, path string, status int, body []byte) {
	t.Helper()
	v, err := decodeJSON(body)
	if err != nil {
		t.Fatalf("%s %s %s: not JSON: %s", s.name, method, path, body)
	}
	var errs []string
	if status >= 400 {
		errs = s.validate(s.errorSchema(), v, "$", false)
		if code, _ := m(m(v)["error"])["code"].(string); code == "" {
			errs = append(errs, "$.error.code: empty")
		}
	} else {
		sch := s.responseSchema(method, path, status)
		if sch == nil {
			t.Fatalf("%s: %s %s: ответ %d не описан", s.name, method, path, status)
		}
		errs = s.validate(sch, v, "$", true)
	}
	if len(errs) > 0 {
		t.Fatalf("%s: %s %s → %d не по схеме:\n%s\n%s", s.name, method, path, status, strings.Join(errs, "\n"), body)
	}
}
