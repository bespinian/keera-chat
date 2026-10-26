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

// Config is Keera Chat's configuration.
type Config struct {
	// Addr serves the page at /, the API at /api and sign-in at /auth.
	Addr string

	// PublicURL is chat's origin as a browser sees it. It must match the
	// gateway's KEERA_CHAT_URL, since sign-in returns to /auth/callback.
	PublicURL string

	// GatewayURL is the gateway's origin as a browser sees it.
	GatewayURL string
	// GatewayInternalURL is how chat reaches the gateway, such as on a
	// compose network. Defaults to GatewayURL.
	GatewayInternalURL string

	// SecretKey seals chat's cookies. Changing it signs everyone out.
	SecretKey string

	// SecureCookies defaults to on for an https PublicURL. Off allows plain
	// http on localhost.
	SecureCookies bool

	// MaxBodyBytes limits one API request. It is large because each message
	// carries the whole conversation, images included.
	MaxBodyBytes int64

	LogLevel  string
	LogFormat string
}

// Load reads the configuration from the environment.
func Load() (Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (Config, error) {
	env := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	originEnv := func(key, def string) (string, error) { return origin(key, env(key, def)) }

	c := Config{
		Addr:      env("KEERA_CHAT_ADDR", ":8090"),
		SecretKey: env("KEERA_CHAT_SECRET_KEY", ""),
		LogLevel:  env("KEERA_CHAT_LOG_LEVEL", "info"),
		LogFormat: env("KEERA_CHAT_LOG_FORMAT", "text"),
	}

	var err error
	if c.GatewayURL, err = originEnv("KEERA_CHAT_GATEWAY_URL", ""); err != nil {
		return c, err
	}
	if c.GatewayURL == "" {
		return c, errors.New("KEERA_CHAT_GATEWAY_URL is required: the address a browser " +
			"reaches Keera Gateway on, such as https://keera.example.ch")
	}
	if c.GatewayInternalURL, err = originEnv("KEERA_CHAT_GATEWAY_INTERNAL_URL", c.GatewayURL); err != nil {
		return c, err
	}

	// Default to loopback. The gateway allows it without setup, which suits
	// development.
	def := ""
	if _, port, err := net.SplitHostPort(c.Addr); err == nil && port != "" {
		def = "http://127.0.0.1:" + port
	}
	if c.PublicURL, err = originEnv("KEERA_CHAT_PUBLIC_URL", def); err != nil {
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

// origin returns raw as a bare http(s) origin. Paths are refused: chat and
// the gateway each run at the root of their host.
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
