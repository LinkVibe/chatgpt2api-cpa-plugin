package main

import "encoding/json"

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// okEnvelope wraps v as {"ok":true,"result":<v>}.
//
// Built by splicing rather than marshalling an envelope{Result: json.RawMessage}:
// the nested form re-scans and re-copies the whole payload through a buffer that
// grows by doubling, which for a multi-MB image response peaked at ~2.6x the
// payload. Splicing with an exact capacity keeps it at one extra copy.
func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	const prefix = `{"ok":true,"result":`
	out := make([]byte, 0, len(prefix)+len(raw)+1)
	out = append(out, prefix...)
	out = append(out, raw...)
	return append(out, '}'), nil
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func decodeEnvelope(raw []byte) (json.RawMessage, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if !env.OK {
		msg := "host call failed"
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return nil, errString(msg)
	}
	return env.Result, nil
}

type errString string

func (e errString) Error() string { return string(e) }
