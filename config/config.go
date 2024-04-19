package config

type ServiceType string

const TCP ServiceType = "tcp"
const UDP ServiceType = "udp"

type ClientServiceConfig struct {
	Type      ServiceType `toml:"type"`
	Token     string      `toml:"token"`
	LocalAddr string      `toml:"local_addr"`
}

type ClientConfig struct {
	RemoteAddr string `toml:"remote_addr"`

	Services map[string]ClientServiceConfig
}

type ServerServiceConfig struct {
	Type     ServiceType `toml:"type"`
	Token    string      `toml:"token"`
	BindAddr string      `toml:"bind_addr"`
}

type ServerConfig struct {
	BindAddr string `toml:"bind_addr"`

	Services map[string]ServerServiceConfig
}

type Config struct {
	Client ClientConfig
	Server ServerConfig
}
