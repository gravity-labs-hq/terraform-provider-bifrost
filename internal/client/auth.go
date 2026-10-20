package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Authenticator entrega el Bearer para /v1. Las implementaciones cachean el token hasta poco antes de que venza.
type Authenticator interface {
	Token(ctx context.Context, c *Client) (string, error)
}

// Concesiones de /auth/token (mismas cadenas que internal/plane/wire del servidor).
const (
	GrantServiceAccount = "service_account"
	GrantFederated      = "federated"
	GrantUser           = "user"
)

// KeyType es el valor de ServiceAccountKey.Type.
const KeyType = "bifrost_service_account"

// assertionTTL: plane rechaza aserciones de más de 5 minutos.
const assertionTTL = 4 * time.Minute

// refreshMargin: un token se reutiliza hasta 5 minutos antes de caducar.
const refreshMargin = 5 * time.Minute

// ServiceAccountKey es el archivo de clave de una cuenta de servicio (el que entrega Bifröst una sola vez).
type ServiceAccountKey struct {
	Type         string `json:"type"`
	API          string `json:"api"`
	Account      string `json:"account"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
}

// ParseKeyJSON valida el JSON de una clave y la devuelve. Los errores no incluyen el material de la clave.
func ParseKeyJSON(b []byte) (ServiceAccountKey, error) {
	var k ServiceAccountKey
	if err := json.Unmarshal(b, &k); err != nil {
		return k, errors.New("la clave no es un JSON válido")
	}
	if _, err := k.privateKey(); err != nil {
		return ServiceAccountKey{}, err
	}
	return k, nil
}

func (k ServiceAccountKey) privateKey() (ed25519.PrivateKey, error) {
	if k.Type != KeyType {
		return nil, errors.New("el archivo no es una clave de cuenta de servicio de Bifröst")
	}
	if k.Account == "" || k.PrivateKeyID == "" || k.API == "" {
		return nil, errors.New("la clave está incompleta")
	}
	blk, _ := pem.Decode([]byte(k.PrivateKey))
	if blk == nil {
		return nil, errors.New("la clave privada no está en formato PEM")
	}
	pk, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, errors.New("la clave privada no es PKCS#8 válida")
	}
	ed, ok := pk.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("la clave privada no es Ed25519")
	}
	return ed, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// SignAssertion firma el JWT de aserción: EdDSA, kid = private_key_id, iss = sub = cuenta, aud = token_uri, exp <= 5 min, jti único.
func SignAssertion(k ServiceAccountKey, tokenURI string, now time.Time) (string, error) {
	priv, err := k.privateKey()
	if err != nil {
		return "", err
	}
	var j [12]byte
	if _, err := rand.Read(j[:]); err != nil {
		return "", err
	}
	hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": k.PrivateKeyID})
	cb, _ := json.Marshal(map[string]any{"iss": k.Account, "sub": k.Account, "aud": tokenURI, "iat": now.Unix(), "nbf": now.Add(-10 * time.Second).Unix(),
		"exp": now.Add(assertionTTL).Unix(), "jti": hex.EncodeToString(j[:])})
	in := b64(hb) + "." + b64(cb)
	return in + "." + b64(ed25519.Sign(priv, []byte(in))), nil
}

type tokenRequest struct {
	Grant     string `json:"grant"`
	Assertion string `json:"assertion,omitempty"`
	IDToken   string `json:"id_token,omitempty"`
	App       string `json:"app,omitempty"`
	Platform  string `json:"platform,omitempty"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// cached cachea un token con su vencimiento.
type cached struct {
	mu  sync.Mutex
	tok string
	exp time.Time
	now func() time.Time
}

func (c *cached) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *cached) get(fetch func() (string, time.Duration, error)) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" && c.clock().Add(refreshMargin).Before(c.exp) {
		return c.tok, nil
	}
	tok, ttl, err := fetch()
	if err != nil {
		return "", err
	}
	c.tok, c.exp = tok, c.clock().Add(ttl)
	return tok, nil
}

func exchange(ctx context.Context, c *Client, req tokenRequest) (string, time.Duration, error) {
	d, err := c.Discover(ctx)
	if err != nil {
		return "", 0, err
	}
	var out tokenResponse
	if err := c.raw(ctx, http.MethodPost, d.TokenURI, "", nil, req, &out); err != nil {
		return "", 0, errors.New("plane rechazó la credencial: " + explain(err))
	}
	if out.AccessToken == "" {
		return "", 0, errors.New("plane no entregó token")
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return out.AccessToken, ttl, nil
}

func explain(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

// StaticToken usa un Bearer ya emitido (BIFROST_TOKEN). No se renueva: al vencer, la API responde 401.
type StaticToken string

func (s StaticToken) Token(context.Context, *Client) (string, error) {
	if s == "" {
		return "", errors.New("el token está vacío")
	}
	return string(s), nil
}

// KeyAuth canjea la clave de una cuenta de servicio (grant service_account).
type KeyAuth struct {
	Key ServiceAccountKey
	c   cached
}

func (k *KeyAuth) Token(ctx context.Context, c *Client) (string, error) {
	return k.c.get(func() (string, time.Duration, error) {
		d, err := c.Discover(ctx)
		if err != nil {
			return "", 0, err
		}
		as, err := SignAssertion(k.Key, d.TokenURI, time.Now())
		if err != nil {
			return "", 0, err
		}
		return exchange(ctx, c, tokenRequest{Grant: GrantServiceAccount, Assertion: as})
	})
}

// IDTokenAuth canjea un ID token (federación del CI o persona). Read se invoca en cada canje: así un ID token renovado por el CI se recoge.
type IDTokenAuth struct {
	Grant    string // GrantFederated | GrantUser
	App      string // solo federated: App destino del vínculo
	Platform string // solo federated: cuenta de servicio de PLATAFORMA que el job de CI asume (vínculo de la cuenta, no de una App)
	Read     func() (string, error)
	c        cached
}

func (a *IDTokenAuth) Token(ctx context.Context, c *Client) (string, error) {
	return a.c.get(func() (string, time.Duration, error) {
		idt, err := a.Read()
		if err != nil {
			return "", 0, err
		}
		idt = strings.TrimSpace(idt)
		if idt == "" {
			return "", 0, errors.New("el ID token está vacío")
		}
		return exchange(ctx, c, tokenRequest{Grant: a.Grant, IDToken: idt, App: a.App, Platform: a.Platform})
	})
}
