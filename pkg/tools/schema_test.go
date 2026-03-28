package tools

import (
	"reflect"
	"testing"

	"github.com/vvsynapse/agent-sdk-go/pkg/interfaces"
)

func TestParamString(t *testing.T) {
	s := ParamString("a string param")
	want := interfaces.JSONSchema{"type": "string", "description": "a string param"}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("ParamString = %v, want %v", s, want)
	}
}

func TestParamInteger(t *testing.T) {
	s := ParamInteger("an int param")
	want := interfaces.JSONSchema{"type": "integer", "description": "an int param"}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("ParamInteger = %v, want %v", s, want)
	}
}

func TestParamNumber(t *testing.T) {
	s := ParamNumber("a number param")
	want := interfaces.JSONSchema{"type": "number", "description": "a number param"}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("ParamNumber = %v, want %v", s, want)
	}
}

func TestParamBool(t *testing.T) {
	s := ParamBool("a bool param")
	want := interfaces.JSONSchema{"type": "boolean", "description": "a bool param"}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("ParamBool = %v, want %v", s, want)
	}
}

func TestParamEnum(t *testing.T) {
	s := ParamEnum("choose one", "a", "b", "c")
	want := interfaces.JSONSchema{"type": "string", "description": "choose one", "enum": []any{"a", "b", "c"}}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("ParamEnum = %v, want %v", s, want)
	}
}

func TestParamArray(t *testing.T) {
	items := ParamString("item")
	s := ParamArray("list of items", items)
	want := interfaces.JSONSchema{
		"type":        "array",
		"description": "list of items",
		"items":       items,
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("ParamArray = %v, want %v", s, want)
	}
}

func TestParams(t *testing.T) {
	props := map[string]interfaces.JSONSchema{
		"x": ParamString("x desc"),
		"y": ParamInteger("y desc"),
	}
	s := Params(props, "x")

	if s["type"] != "object" {
		t.Errorf("Params type = %v, want object", s["type"])
	}
	if _, ok := s["properties"]; !ok {
		t.Error("Params should have properties")
	}
	req, ok := s["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "x" {
		t.Errorf("Params required = %v, want [x]", s["required"])
	}
}

func TestParams_NoRequired(t *testing.T) {
	s := Params(map[string]interfaces.JSONSchema{"a": ParamString("a")})
	if _, ok := s["required"]; ok {
		t.Error("Params with no required should not have required field")
	}
}
