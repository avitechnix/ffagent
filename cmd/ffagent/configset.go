package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/avitechnix/ffagent/internal/config"
)

// configSet changes one setting in a JSON config file, keeping the file's
// key order, and refuses a result the agent wouldn't load. The key is a
// dotted path (web.enabled); the value is JSON (true, 12, ["a"]) or else a
// plain string. Used by the macOS scripts, whose sed differs from Linux's.
func configSet(path, key, value string) error {
	if path == "" {
		return errors.New("config set: no config file (set --config or FFAGENT_CONFIG)")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	root, err := parseOrdered(b)
	if err != nil {
		return fmt.Errorf("config set: %s: %w", path, err)
	}
	var v any = value
	if json.Valid([]byte(value)) {
		v = json.RawMessage(value)
	}
	obj, ok := root.(*ordered)
	if !ok {
		return fmt.Errorf("config set: %s isn't a JSON object", path)
	}
	parts := strings.Split(key, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := obj.get(p).(*ordered)
		if !ok {
			next = &ordered{}
			obj.set(p, next)
		}
		obj = next
	}
	obj.set(parts[len(parts)-1], v)

	var out bytes.Buffer
	writeOrdered(&out, root, "")
	out.WriteByte('\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if fi, err := os.Stat(path); err == nil {
		tmp.Chmod(fi.Mode().Perm())
	}
	if _, err := tmp.Write(out.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if _, err := config.Load(tmp.Name()); err != nil {
		return fmt.Errorf("config set: %s=%s would make the config invalid: %w", key, value, err)
	}
	return os.Rename(tmp.Name(), path)
}

// ordered is a JSON object that remembers its key order.
type ordered struct {
	keys []string
	vals map[string]any
}

func (o *ordered) get(k string) any { return o.vals[k] }

func (o *ordered) set(k string, v any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func parseOrdered(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data")
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		o := &ordered{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			o.set(kt.(string), v)
		}
		_, err := dec.Token()
		return o, err
	case json.Delim('['):
		var a []any
		for dec.More() {
			v, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err := dec.Token()
		return a, err
	}
	return t, nil
}

func writeOrdered(w *bytes.Buffer, v any, indent string) {
	switch x := v.(type) {
	case *ordered:
		if len(x.keys) == 0 {
			w.WriteString("{}")
			return
		}
		w.WriteString("{\n")
		for i, k := range x.keys {
			kb, _ := json.Marshal(k)
			w.WriteString(indent + "  ")
			w.Write(kb)
			w.WriteString(": ")
			writeOrdered(w, x.vals[k], indent+"  ")
			if i < len(x.keys)-1 {
				w.WriteByte(',')
			}
			w.WriteByte('\n')
		}
		w.WriteString(indent + "}")
	case []any:
		if len(x) == 0 {
			w.WriteString("[]")
			return
		}
		w.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				w.WriteString(", ")
			}
			writeOrdered(w, e, indent)
		}
		w.WriteByte(']')
	case json.RawMessage:
		var c bytes.Buffer
		if json.Compact(&c, x) == nil {
			// Re-read so a nested object is laid out like the rest.
			if p, err := parseOrdered(c.Bytes()); err == nil {
				if _, isRaw := p.(json.RawMessage); !isRaw {
					writeOrdered(w, p, indent)
					return
				}
			}
		}
		w.Write(x)
	default:
		b, _ := json.Marshal(x)
		w.Write(b)
	}
}
