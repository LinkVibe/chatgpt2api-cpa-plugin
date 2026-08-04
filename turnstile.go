package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

type orderedMap struct {
	keys   []string
	values map[string]any
}

func newOrderedMap() *orderedMap {
	return &orderedMap{values: map[string]any{}}
}

func (o *orderedMap) add(key string, value any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func turnstileToStr(value any) string {
	if value == nil {
		return "undefined"
	}
	switch v := value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		special := map[string]string{
			"window.Math":            "[object Math]",
			"window.Reflect":         "[object Reflect]",
			"window.performance":     "[object Performance]",
			"window.localStorage":    "[object Storage]",
			"window.Object":          "function Object() { [native code] }",
			"window.Reflect.set":     "function set() { [native code] }",
			"window.performance.now": "function () { [native code] }",
			"window.Object.create":   "function create() { [native code] }",
			"window.Object.keys":     "function keys() { [native code] }",
			"window.Math.random":     "function random() { [native code] }",
		}
		if s, ok := special[v]; ok {
			return s
		}
		return v
	case []string:
		return strings.Join(v, ",")
	case []any:
		parts := make([]string, 0, len(v))
		allStr := true
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				allStr = false
				break
			}
			parts = append(parts, s)
		}
		if allStr {
			return strings.Join(parts, ",")
		}
		return fmt.Sprint(v)
	default:
		return fmt.Sprint(v)
	}
}

func xorString(text, key string) string {
	if key == "" {
		return text
	}
	out := make([]byte, len(text))
	for i := 0; i < len(text); i++ {
		out[i] = text[i] ^ key[i%len(key)]
	}
	return string(out)
}

func solveTurnstileToken(dx, p string) string {
	decoded, err := base64.StdEncoding.DecodeString(dx)
	if err != nil {
		return ""
	}
	var tokenList [][]any
	if err := json.Unmarshal([]byte(xorString(string(decoded), p)), &tokenList); err != nil {
		return ""
	}

	processMap := map[float64]any{}
	start := time.Now()
	result := ""

	asF := func(v any) float64 {
		switch t := v.(type) {
		case float64:
			return t
		case int:
			return float64(t)
		case json.Number:
			f, _ := t.Float64()
			return f
		default:
			f, _ := strconv.ParseFloat(fmt.Sprint(v), 64)
			return f
		}
	}

	func1 := func(e, t float64) {
		processMap[e] = xorString(turnstileToStr(processMap[e]), turnstileToStr(processMap[t]))
	}
	func2 := func(e float64, t any) { processMap[e] = t }
	func3 := func(e string) {
		result = base64.StdEncoding.EncodeToString([]byte(e))
	}
	func5 := func(e, t float64) {
		current := processMap[e]
		incoming := processMap[t]
		switch c := current.(type) {
		case []any:
			processMap[e] = append(c, incoming)
			return
		case []string:
			processMap[e] = append(c, turnstileToStr(incoming))
			return
		}
		_, cStr := current.(string)
		_, cF := current.(float64)
		_, iStr := incoming.(string)
		_, iF := incoming.(float64)
		if cStr || cF || iStr || iF {
			processMap[e] = turnstileToStr(current) + turnstileToStr(incoming)
			return
		}
		processMap[e] = "NaN"
	}
	func6 := func(e, t, n float64) {
		tv, _ := processMap[t].(string)
		nv, _ := processMap[n].(string)
		if tv != "" || nv != "" {
			value := tv + "." + nv
			if value == "window.document.location" {
				processMap[e] = "https://chatgpt.com/"
			} else {
				processMap[e] = value
			}
		}
	}
	func7 := func(e float64, args ...float64) {
		target := processMap[e]
		values := make([]any, 0, len(args))
		for _, a := range args {
			values = append(values, processMap[a])
		}
		if s, ok := target.(string); ok && s == "window.Reflect.set" {
			if len(values) >= 3 {
				if obj, ok := values[0].(*orderedMap); ok {
					obj.add(fmt.Sprint(values[1]), values[2])
				}
			}
			return
		}
		if fn, ok := target.(func(...any)); ok {
			fn(values...)
		}
	}
	func8 := func(e, t float64) { processMap[e] = processMap[t] }
	func14 := func(e, t float64) {
		var v any
		_ = json.Unmarshal([]byte(turnstileToStr(processMap[t])), &v)
		processMap[e] = v
	}
	func15 := func(e, t float64) {
		raw, _ := json.Marshal(processMap[t])
		processMap[e] = string(raw)
	}
	func17 := func(e, t float64, args ...float64) {
		callArgs := make([]any, 0, len(args))
		for _, a := range args {
			callArgs = append(callArgs, processMap[a])
		}
		target := processMap[t]
		switch target {
		case "window.performance.now":
			elapsed := float64(time.Since(start).Nanoseconds()) + rand.Float64()
			processMap[e] = elapsed / 1e6
		case "window.Object.create":
			processMap[e] = newOrderedMap()
		case "window.Object.keys":
			if len(callArgs) > 0 && callArgs[0] == "window.localStorage" {
				processMap[e] = []string{
					"STATSIG_LOCAL_STORAGE_INTERNAL_STORE_V4",
					"STATSIG_LOCAL_STORAGE_STABLE_ID",
					"client-correlated-secret",
					"oai/apps/capExpiresAt",
					"oai-did",
					"STATSIG_LOCAL_STORAGE_LOGGING_REQUEST",
					"UiState.isNavigationCollapsed.1",
				}
			}
		case "window.Math.random":
			processMap[e] = rand.Float64()
		default:
			if fn, ok := target.(func(...any) any); ok {
				processMap[e] = fn(callArgs...)
			}
		}
	}
	func18 := func(e float64) {
		decoded, err := base64.StdEncoding.DecodeString(turnstileToStr(processMap[e]))
		if err == nil {
			processMap[e] = string(decoded)
		}
	}
	func19 := func(e float64) {
		processMap[e] = base64.StdEncoding.EncodeToString([]byte(turnstileToStr(processMap[e])))
	}
	func20 := func(e, t, n float64, args ...float64) {
		if processMap[e] == processMap[t] {
			if fn, ok := processMap[n].(func(...float64)); ok {
				fn(args...)
			}
		}
	}
	func21 := func(...any) {}
	func23 := func(e, t float64, args ...float64) {
		if processMap[e] != nil {
			if fn, ok := processMap[t].(func(...float64)); ok {
				fn(args...)
			}
		}
	}
	func24 := func(e, t, n float64) {
		tv, _ := processMap[t].(string)
		nv, _ := processMap[n].(string)
		if tv != "" || nv != "" {
			processMap[e] = tv + "." + nv
		}
	}

	processMap[1] = func1
	processMap[2] = func2
	processMap[3] = func3
	processMap[5] = func5
	processMap[6] = func6
	processMap[7] = func7
	processMap[8] = func8
	processMap[9] = tokenList
	processMap[10] = "window"
	processMap[14] = func14
	processMap[15] = func15
	processMap[16] = p
	processMap[17] = func17
	processMap[18] = func18
	processMap[19] = func19
	processMap[20] = func20
	processMap[21] = func21
	processMap[23] = func23
	processMap[24] = func24

	for _, token := range tokenList {
		if len(token) == 0 {
			continue
		}
		fnID := asF(token[0])
		fn := processMap[fnID]
		args := token[1:]
		switch f := fn.(type) {
		case func(float64, float64):
			if len(args) >= 2 {
				f(asF(args[0]), asF(args[1]))
			}
		case func(float64, any):
			if len(args) >= 2 {
				f(asF(args[0]), args[1])
			}
		case func(string):
			if len(args) >= 1 {
				f(fmt.Sprint(args[0]))
			}
		case func(float64, float64, float64):
			if len(args) >= 3 {
				f(asF(args[0]), asF(args[1]), asF(args[2]))
			}
		case func(float64, ...float64):
			fs := make([]float64, 0, len(args))
			for _, a := range args {
				fs = append(fs, asF(a))
			}
			if len(fs) > 0 {
				f(fs[0], fs[1:]...)
			}
		case func(float64, float64, ...float64):
			if len(args) >= 2 {
				rest := make([]float64, 0, len(args)-2)
				for _, a := range args[2:] {
					rest = append(rest, asF(a))
				}
				f(asF(args[0]), asF(args[1]), rest...)
			}
		case func(float64, float64, float64, ...float64):
			if len(args) >= 3 {
				rest := make([]float64, 0, len(args)-3)
				for _, a := range args[3:] {
					rest = append(rest, asF(a))
				}
				f(asF(args[0]), asF(args[1]), asF(args[2]), rest...)
			}
		case func(float64):
			if len(args) >= 1 {
				f(asF(args[0]))
			}
		case func(...any):
			f()
		case func(...float64):
			fs := make([]float64, 0, len(args))
			for _, a := range args {
				fs = append(fs, asF(a))
			}
			f(fs...)
		default:
			// ignore unknown
		}
	}
	return result
}
