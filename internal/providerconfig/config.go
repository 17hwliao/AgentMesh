// Package providerconfig validates provider model bindings without credentials,
// adapter construction or network access. Gateway and runner share this contract.
package providerconfig

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var ErrMissing = errors.New("provider_configuration_missing")
var ErrInvalid = errors.New("provider_configuration_invalid")
var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,128}$`)

func AliasValid(value string) bool { return aliasPattern.MatchString(value) }
func ModelValid(value string) bool { return modelPattern.MatchString(value) }
func KeyValid(value string) bool {
	if value == "" || len(value) > 16384 {
		return false
	}
	for _, b := range []byte(value) {
		if b <= 32 || b >= 127 {
			return false
		}
	}
	return true
}

func BaseURL(raw string) (string, error) {
	p, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Hostname() == "" || p.User != nil || p.RawQuery != "" || p.ForceQuery || p.Fragment != "" || p.Opaque != "" {
		return "", ErrInvalid
	}
	if p.Port() != "" {
		port, e := strconv.Atoi(p.Port())
		if e != nil || port < 1 || port > 65535 {
			return "", ErrInvalid
		}
	}
	return strings.TrimRight(p.String(), "/"), nil
}

// Models rejects duplicate keys instead of allowing JSON's last-value-wins
// behavior to silently change an upstream model and its associated tariff.
func Models(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, ErrMissing
	}
	if len(raw) > 16384 {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(strings.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalid
	}
	models := map[string]string{}
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return nil, ErrInvalid
		}
		alias, ok := key.(string)
		if !ok || !AliasValid(alias) {
			return nil, ErrInvalid
		}
		var model string
		if d.Decode(&model) != nil || !ModelValid(model) || len(models) >= 32 {
			return nil, ErrInvalid
		}
		if _, exists := models[alias]; exists {
			return nil, ErrInvalid
		}
		models[alias] = model
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF || len(models) == 0 {
		return nil, ErrInvalid
	}
	return models, nil
}

type Binding struct {
	Name                                    string
	Model                                   string
	Models                                  map[string]string
	DisableThinking, UseMaxCompletionTokens bool
}

func Bindings(order []string, lookup func(string) string) ([]Binding, error) {
	if lookup == nil {
		return nil, ErrMissing
	}
	result := make([]Binding, 0, len(order))
	for _, name := range order {
		b := Binding{Name: name}
		switch name {
		case "ark":
			b.Model = lookup("ARK_MODEL")
		case "ollama":
			b.Model = lookup("OLLAMA_MODEL")
		case "compatible":
			for key, target := range map[string]*bool{"COMPATIBLE_DISABLE_THINKING": &b.DisableThinking, "COMPATIBLE_USE_MAX_COMPLETION_TOKENS": &b.UseMaxCompletionTokens} {
				switch lookup(key) {
				case "", "false":
				case "true":
					*target = true
				default:
					return nil, ErrInvalid
				}
			}
			var err error
			b.Models, err = Models(lookup("COMPATIBLE_MODELS"))
			if err != nil {
				return nil, err
			}
		default:
			return nil, ErrInvalid
		}
		if name != "compatible" {
			if b.Model == "" {
				return nil, ErrMissing
			}
			if !ModelValid(b.Model) {
				return nil, ErrInvalid
			}
		}
		result = append(result, b)
	}
	return result, nil
}
