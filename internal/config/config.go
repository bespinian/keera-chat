// Package config reads Keera Chat's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Config is the whole of Keera Chat's configuration.
type Config struct {
	// Addr is the one listener: the chat at /, its API under /api and the
	// sign-in under /auth.
	Addr string

	// PublicURL is this service's origin as a browser sees it. The gateway
	// sends the browser back to PublicURL + "/auth/callback" after a sign-in,
	// so it must match what the gateway was told in KEERA_CHAT_URL.
	PublicURL string

	// GatewayURL is Keera Gateway's origin as a browser sees it. The sign-in
	// starts there, and the account menu links to its panel.
	GatewayURL string
	// GatewayInternalURL is where this service itself reaches the gateway,
	// for a deployment where that differs, such as a compose network. It
	// defaults to GatewayURL.
	GatewayInternalURL string

	// SecretKey seals chat's cookies. Changing it signs everyone out.
	SecretKey string

	// SecureCookies marks chat's cookies Secure. On by default when the
	// public URL is https; off lets a plain-http setup on localhost sign in.
	SecureCookies bool

	// MaxBodyBytes bounds one request to the chat API. Each message brings
	// its whole conversation, images inline, so it is far above what text
	// needs.
	MaxBodyBytes int64

	LogLevel  string
	LogFormat string
}

// Load reads the configuration from the process environment.
func Load() (Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (Config, error) {
	env := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	c := Config{
		Addr:      env("KEERA_CHAT_ADDR", ":8090"),
		SecretKey: env("KEERA_CHAT_SECRET_KEY", ""),
		LogLevel:  env("KEERA_CHAT_LOG_LEVEL", "info"),
		LogFormat: env("KEERA_CHAT_LOG_FORMAT", "text"),
	}

	var err error
	if c.GatewayURL, err = origin("KEERA_CHAT_GATEWAY_URL", env("KEERA_CHAT_GATEWAY_URL", "")); err != nil {
		return c, err
	}
	if c.GatewayURL == "" {
		return c, errors.New("KEERA_CHAT_GATEWAY_URL is required: the address a browser " +
			"reaches Keera Gateway on, such as https://keera.example.ch")
	}
	if c.GatewayInternalURL, err = origin("KEERA_CHAT_GATEWAY_INTERNAL_URL",
		env("KEERA_CHAT_GATEWAY_INTERNAL_URL", c.GatewayURL)); err != nil {
		return c, err
	}

	// Without a public URL, assume a browser on this machine. The gateway
	// accepts a sign-in that comes back to a loopback address without being
	// told about it, so this works out of the box for development.
	def := ""
	if _, port, err := net.SplitHostPort(c.Addr); err == nil && port != "" {
		def = "http://127.0.0.1:" + port
	}
	if c.PublicURL, err = origin("KEERA_CHAT_PUBLIC_URL", env("KEERA_CHAT_PUBLIC_URL", def)); err != nil {
		return c, err
	}
	if c.PublicURL == "" {
		return c, errors.New("KEERA_CHAT_PUBLIC_URL is required: the address a browser " +
			"reaches Keera Chat on, such as https://chat.example.ch")
	}

	if len(c.SecretKey) < 16 {
		return c, errors.New("KEERA_CHAT_SECRET_KEY is required and must be at least 16 " +
			"characters; generate one with: openssl rand -hex 32")
	}

	c.SecureCookies = strings.HasPrefix(c.PublicURL, "https://")
	if v := env("KEERA_CHAT_SECURE_COOKIES", ""); v != "" {
		if c.SecureCookies, err = strconv.ParseBool(v); err != nil {
			return c, fmt.Errorf("KEERA_CHAT_SECURE_COOKIES must be true or false, not %q", v)
		}
	}

	c.MaxBodyBytes = 32 << 20
	if v := env("KEERA_CHAT_MAX_BODY_BYTES", ""); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1<<20 {
			return c, fmt.Errorf("KEERA_CHAT_MAX_BODY_BYTES must be a number of bytes, "+
				"at least 1048576, not %q", v)
		}
		c.MaxBodyBytes = n
	}

	if !slices.Contains([]string{"debug", "info", "warn", "error"}, c.LogLevel) {
		return c, fmt.Errorf("KEERA_CHAT_LOG_LEVEL must be debug, info, warn or error, not %q", c.LogLevel)
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		return c, fmt.Errorf("KEERA_CHAT_LOG_FORMAT must be text or json, not %q", c.LogFormat)
	}
	return c, nil
}

// origin checks that raw is a bare http or https origin and returns it
// without a trailing slash. Paths are refused: the chat and the gateway are
// each reached at the root of their host.
func origin(name, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.Trim(u.Path, "/") != "" {
		return "", fmt.Errorf("%s must be an http or https address without a path, "+
			"such as https://host.example.ch, not %q", name, raw)
	}
	return u.Scheme + "://" + u.Host, nil
}
