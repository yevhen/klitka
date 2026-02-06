package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	klitkav1 "github.com/klitka/klitka/proto/gen/go/klitka/v1"
)

type secretSpec struct {
	name        string
	hosts       []string
	value       string
	header      string
	format      klitkav1.SecretFormat
	placeholder string
}

func buildSecrets(secrets []*klitkav1.Secret) ([]secretSpec, []string, error) {
	if len(secrets) == 0 {
		return nil, nil, nil
	}

	out := make([]secretSpec, 0, len(secrets))
	env := make([]string, 0, len(secrets))
	seen := map[string]struct{}{}

	for _, secret := range secrets {
		name := strings.TrimSpace(secret.GetName())
		if name == "" {
			return nil, nil, fmt.Errorf("secret name is required")
		}
		if _, ok := seen[name]; ok {
			return nil, nil, fmt.Errorf("duplicate secret name: %s", name)
		}
		seen[name] = struct{}{}

		value := secret.GetValue()
		if value == "" {
			return nil, nil, fmt.Errorf("secret value is required for %s", name)
		}

		hosts := normalizeHosts(secret.GetHosts())
		if len(hosts) == 0 {
			return nil, nil, fmt.Errorf("secret %s requires at least one host", name)
		}

		header := strings.TrimSpace(secret.GetHeader())
		if header == "" {
			header = "Authorization"
		}
		header = http.CanonicalHeaderKey(header)

		format := secret.GetFormat()
		if format == klitkav1.SecretFormat_SECRET_FORMAT_UNSPECIFIED {
			format = klitkav1.SecretFormat_SECRET_FORMAT_BEARER
		}

		placeholder, err := generatePlaceholder(name)
		if err != nil {
			return nil, nil, err
		}

		out = append(out, secretSpec{
			name:        name,
			hosts:       hosts,
			value:       value,
			header:      header,
			format:      format,
			placeholder: placeholder,
		})
		env = append(env, fmt.Sprintf("%s=%s", name, placeholder))
	}

	return out, env, nil
}

func generatePlaceholder(name string) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("klitka:%s:%s", strings.ToLower(name), hex.EncodeToString(buf)), nil
}

func applySecrets(host string, headers http.Header, secrets []secretSpec) {
	if len(secrets) == 0 {
		return
	}
	cleanHost := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	for _, secret := range secrets {
		if !secret.matchesHost(cleanHost) {
			continue
		}
		secret.apply(headers)
	}
}

func (spec secretSpec) matchesHost(host string) bool {
	for _, pattern := range spec.hosts {
		if matchHost(pattern, host) {
			return true
		}
	}
	return false
}

func (spec secretSpec) apply(headers http.Header) {
	values, ok := headers[spec.header]
	if !ok {
		return
	}
	updated := make([]string, len(values))
	replaced := false
	for i, value := range values {
		if strings.Contains(value, spec.placeholder) {
			replaced = true
			if spec.format == klitkav1.SecretFormat_SECRET_FORMAT_BEARER && value == spec.placeholder {
				value = "Bearer " + spec.value
			} else {
				value = strings.ReplaceAll(value, spec.placeholder, spec.value)
			}
		}
		updated[i] = value
	}
	if !replaced {
		return
	}
	headers.Del(spec.header)
	for _, value := range updated {
		headers.Add(spec.header, value)
	}
}

func shouldMitmHost(host string, secrets []secretSpec) bool {
	cleanHost := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	for _, secret := range secrets {
		if secret.matchesHost(cleanHost) {
			return true
		}
	}
	return false
}
