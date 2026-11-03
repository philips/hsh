package oauthprov

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
)

var MarkerPattern = regexp.MustCompile(`hsh_[0-9a-f]{64}`)

var TokenLikeFields = []string{"access_token", "refresh_token", "id_token", "accessToken", "refreshToken", "idToken"}

type Refresh struct {
	ClientID    string            `json:"client_id"`
	RequestBody string            `json:"request_body,omitempty"`
	URL         string            `json:"url,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`
}

type Seed struct {
	Path     string `json:"path"`
	Mode     string `json:"mode,omitempty"`
	Template string `json:"template"`
}

func (s Seed) FileMode() uint32 {
	if s.Mode == "" {
		return 0o600
	}
	m, err := strconv.ParseUint(s.Mode, 8, 32)
	if err != nil {
		return 0o600
	}
	return uint32(m)
}

func (r *Refresh) validate() error {
	if r.ClientID == "" {
		return fmt.Errorf("refresh.client_id is empty")
	}
	switch r.RequestBody {
	case "", "form", "json":
	default:
		return fmt.Errorf("refresh.request_body %q (want form or json)", r.RequestBody)
	}
	if r.URL != "" {
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("refresh.url %q is not an http(s) URL", r.URL)
		}
	}
	return nil
}

func (s Seed) validate(name string) error {
	if !strings.HasPrefix(s.Path, "/") {
		return fmt.Errorf("seed %q: path %q must be absolute", name, s.Path)
	}
	if s.Template == "" {
		return fmt.Errorf("seed %q: template is empty", name)
	}
	if s.Mode != "" {
		if _, err := strconv.ParseUint(s.Mode, 8, 32); err != nil {
			return fmt.Errorf("seed %q: mode %q is not octal", name, s.Mode)
		}
	}
	if _, err := seedTemplate(name, s.Template); err != nil {
		return fmt.Errorf("seed %q: %w", name, err)
	}
	return nil
}

func seedTemplate(name, text string) (*template.Template, error) {
	return template.New(name).Option("missingkey=zero").Funcs(template.FuncMap{
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
		"split": func(s any) []string {
			str, _ := s.(string)
			return strings.Fields(str)
		},
	}).Parse(text)
}

func RenderSeed(name string, s Seed, data map[string]any) ([]byte, error) {
	t, err := seedTemplate(name, s.Template)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("seed %q: %w", name, err)
	}
	return buf.Bytes(), nil
}

func JWTHeaderPayload(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false
	}
	return parts[0] + "." + parts[1], true
}

func JWTExpiry(token string) (time.Time, bool) {
	hp, ok := JWTHeaderPayload(token)
	if !ok {
		return time.Time{}, false
	}
	payload, _ := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.SplitN(hp, ".", 2)[1], "="))
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	n, err := claims.Exp.Int64()
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

func Present(f Field, real, marker string) string {
	if f.Kind == "jwt" {
		if hp, ok := JWTHeaderPayload(real); ok {
			return hp + "." + marker
		}
	}
	if f.Format != "" {
		return strings.Replace(f.Format, PhantomPlaceholder, marker, 1)
	}
	return marker
}

type TokenResult struct {
	Tokens    map[string]string
	Meta      map[string]string
	ExpiresAt time.Time
}

func ExpiresField(name string) string {
	if name == "" {
		return "expires_in"
	}
	return name
}

func ParseTokenResponse(doc map[string]any, fields []Field, expiresField string, now time.Time) TokenResult {
	r := TokenResult{Tokens: map[string]string{}, Meta: map[string]string{}}
	declared := map[string]bool{}
	for _, f := range fields {
		declared[f.Path] = true
		if v, ok := doc[f.Path].(string); ok && v != "" {
			r.Tokens[f.Path] = v
		}
	}
	tokenLike := map[string]bool{}
	for _, k := range TokenLikeFields {
		tokenLike[k] = true
	}
	ef := ExpiresField(expiresField)
	for k, v := range doc {
		if declared[k] || tokenLike[k] || k == ef {
			continue
		}
		switch x := v.(type) {
		case string:
			r.Meta[k] = x
		case json.Number:
			r.Meta[k] = x.String()
		case float64:
			r.Meta[k] = strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			r.Meta[k] = strconv.FormatBool(x)
		}
	}
	switch x := doc[ef].(type) {
	case json.Number:
		if n, err := x.Float64(); err == nil && n > 0 {
			r.ExpiresAt = now.Add(time.Duration(n * float64(time.Second)))
		}
	case float64:
		if x > 0 {
			r.ExpiresAt = now.Add(time.Duration(x * float64(time.Second)))
		}
	}
	if r.ExpiresAt.IsZero() {
		for _, f := range fields {
			if f.Kind == "jwt" && f.Path == "access_token" {
				if exp, ok := JWTExpiry(r.Tokens[f.Path]); ok {
					r.ExpiresAt = exp
				}
			}
		}
	}
	return r
}

type RefreshError struct {
	Status    int
	Code      string
	Message   string
	Permanent bool
}

func (e *RefreshError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("token endpoint answered %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("token endpoint answered %d: %s", e.Status, e.Message)
}

func (d Descriptor) RefreshURL() string {
	if d.Refresh != nil && d.Refresh.URL != "" {
		return d.Refresh.URL
	}
	return "https://" + d.TokenEndpoint.Host + d.TokenEndpoint.Path
}

func (d Descriptor) RefreshTokens(ctx context.Context, client *http.Client, refreshToken string, now time.Time) (TokenResult, error) {
	if d.Refresh == nil {
		return TokenResult{}, errors.New("the provider descriptor has no refresh section")
	}
	params := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     d.Refresh.ClientID,
		"refresh_token": refreshToken,
	}
	for k, v := range d.Refresh.Extra {
		params[k] = v
	}
	var body []byte
	contentType := "application/x-www-form-urlencoded"
	if d.Refresh.RequestBody == "json" {
		contentType = "application/json"
		var err error
		if body, err = json.Marshal(params); err != nil {
			return TokenResult{}, err
		}
	} else {
		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}
		body = []byte(vals.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.RefreshURL(), bytes.NewReader(body))
	if err != nil {
		return TokenResult{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return TokenResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return TokenResult{}, err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error            any    `json:"error"`
			ErrorDescription string `json:"error_description"`
			Message          string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		code, _ := e.Error.(string)
		msg := e.ErrorDescription
		if msg == "" {
			msg = e.Message
		}
		if msg == "" {
			msg = strings.TrimSpace(string(data))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		permanent := false
		switch code {
		case "invalid_grant", "invalid_client", "unauthorized_client", "invalid_request":
			permanent = resp.StatusCode == 400 || resp.StatusCode == 401
		}
		return TokenResult{}, &RefreshError{Status: resp.StatusCode, Code: code, Message: msg, Permanent: permanent}
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return TokenResult{}, fmt.Errorf("token response is not JSON: %w", err)
	}
	r := ParseTokenResponse(doc, d.ResponseFields, d.ExpiresIn, now)
	if r.Tokens["access_token"] == "" {
		return TokenResult{}, errors.New("token response has no access_token")
	}
	return r, nil
}
