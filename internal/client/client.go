// Package client es el cliente HTTP tipado de la API /v1 de Bifröst (plane). No importa código del servidor: el contrato (rutas y JSON)
// está documentado en docs/inventario-api.md y se verifica con el servidor simulado de internal/fakeapi.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Discovery es la respuesta de GET /.well-known/bifrost.
type Discovery struct {
	Issuer      string `json:"issuer"`
	API         string `json:"api"`
	TokenURI    string `json:"token_uri"`
	SessionsURI string `json:"sessions_uri"`
	Version     string `json:"version"`
}

// APIError es una respuesta de error de la API. Message es el mensaje seguro en español que manda el servidor.
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: %s (HTTP %d)", e.Method, e.Path, e.Message, e.Status)
}

func statusIs(err error, code int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == code
}

// IsNotFound dice si el error es un 404 de la API.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsConflict dice si el error es un 409 de la API.
func IsConflict(err error) bool { return statusIs(err, http.StatusConflict) }

// IsForbidden dice si el error es un 403 de la API.
func IsForbidden(err error) bool { return statusIs(err, http.StatusForbidden) }

// Config es lo necesario para construir un Client.
type Config struct {
	// API es la base de plane (https://plane.ejemplo.com). Solo se admite http para localhost y 127.0.0.1 (pruebas).
	API string
	// Auth entrega el Bearer; nil = sin autenticación (solo pruebas de descubrimiento).
	Auth Authenticator
	// CAPEM son certificados de CA adicionales (PEM) que se suman a los del sistema. La verificación TLS nunca se desactiva.
	CAPEM []byte
	// HTTP permite inyectar un http.Client (pruebas). Si se da, CAPEM se ignora.
	HTTP      *http.Client
	UserAgent string
	// Logf recibe líneas de diagnóstico (método, ruta, código). Nunca recibe cuerpos, cabeceras ni tokens.
	Logf func(format string, args ...any)
	// Retries son los reintentos de las lecturas (GET) ante 429/502/503 o fallos de red. 0 = 3.
	Retries int
}

// Client habla con la API /v1 de un Bifröst.
type Client struct {
	cfg  Config
	hc   *http.Client
	base string

	mu   sync.Mutex
	disc *Discovery
}

// CheckBase normaliza la dirección de la API: exige https (http solo en loopback), sin usuario, parámetros ni barra final.
func CheckBase(base string) (string, error) {
	raw := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(raw, ":") {
		return "", fmt.Errorf("la dirección de la API %q no es válida", base)
	}
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("la dirección de la API %q no es válida", base)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("la dirección de la API %q no admite usuario ni parámetros", base)
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return "", errors.New("la API debe usar https (http solo se admite en localhost y 127.0.0.1)")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// New arma el cliente. No hace red: el descubrimiento se hace en la primera petición.
func New(cfg Config) (*Client, error) {
	base, err := CheckBase(cfg.API)
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, base: base}
	if cfg.Retries <= 0 {
		c.cfg.Retries = 3
	}
	if c.cfg.UserAgent == "" {
		c.cfg.UserAgent = "terraform-provider-bifrost"
	}
	switch {
	case cfg.HTTP != nil:
		c.hc = cfg.HTTP
	default:
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tc := &tls.Config{MinVersion: tls.VersionTLS12}
		if len(cfg.CAPEM) > 0 {
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(cfg.CAPEM) {
				return nil, errors.New("la CA indicada no contiene ningún certificado PEM válido")
			}
			tc.RootCAs = pool
		}
		tr.TLSClientConfig = tc
		c.hc = &http.Client{Timeout: 60 * time.Second, Transport: tr}
	}
	return c, nil
}

// LoadCA lee un archivo PEM de CA.
func LoadCA(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer la CA %q: %w", path, err)
	}
	return b, nil
}

func (c *Client) logf(f string, a ...any) {
	if c.cfg.Logf != nil {
		c.cfg.Logf(f, a...)
	}
}

// Discover consulta (y guarda) /.well-known/bifrost.
func (c *Client) Discover(ctx context.Context) (Discovery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disc != nil {
		return *c.disc, nil
	}
	var d Discovery
	if err := c.raw(ctx, http.MethodGet, c.base+"/.well-known/bifrost", "", nil, nil, &d); err != nil {
		return d, fmt.Errorf("no se pudo consultar %s/.well-known/bifrost: %w", c.base, err)
	}
	if d.TokenURI == "" {
		return d, errors.New("la respuesta de descubrimiento está incompleta (falta token_uri)")
	}
	if d.API == "" {
		d.API = c.base + "/v1"
	}
	c.disc = &d
	return d, nil
}

// Call hace una petición a /v1<path> con el Bearer del cliente y decodifica la respuesta en out (nil = ignorar).
func (c *Client) Call(ctx context.Context, method, path string, q url.Values, body, out any) error {
	d, err := c.Discover(ctx)
	if err != nil {
		return err
	}
	tok := ""
	if c.cfg.Auth != nil {
		tok, err = c.cfg.Auth.Token(ctx, c)
		if err != nil {
			return err
		}
	}
	u := strings.TrimRight(d.API, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	attempts := 1
	if method == http.MethodGet {
		attempts += c.cfg.Retries
	}
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(i) * 300 * time.Millisecond):
			}
		}
		last = c.raw(ctx, method, u, tok, nil, body, out)
		if !retryable(last) {
			return last
		}
	}
	return last
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 429 || ae.Status == 502 || ae.Status == 503
	}
	var ne net.Error
	return errors.As(err, &ne)
}

func (c *Client) raw(ctx context.Context, method, u, bearer string, h http.Header, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	pu, _ := url.Parse(u)
	path := u
	if pu != nil {
		path = pu.Path
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) { // quita la URL del error (podría llevar parámetros como confirm)
			err = ue.Err
		}
		c.logf("%s %s -> error de red", method, path)
		return fmt.Errorf("sin conexión con la API (%s %s): %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	c.logf("%s %s -> %d", method, path, resp.StatusCode)
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		msg := ""
		if json.Unmarshal(data, &e) == nil {
			msg = e.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("la API respondió %d sin mensaje", resp.StatusCode)
		}
		return &APIError{Status: resp.StatusCode, Method: method, Path: path, Message: msg}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("respuesta ilegible de %s %s", method, path)
	}
	return nil
}
