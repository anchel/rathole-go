package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type ServiceType string

const TCP ServiceType = "tcp"
const UDP ServiceType = "udp"

var configPath string

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
	BindAddr     string `toml:"bind_addr"`
	AuthUsername string `toml:"auth_username"`
	AuthPassword string `toml:"auth_password"`

	Services map[string]ServerServiceConfig
}

type Config struct {
	Client ClientConfig
	Server ServerConfig
}

func SetConfigPath(p string) {
	configPath = p
}

func GetConfig() (*Config, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Println("ReadFile error", err)
		return nil, errors.New("加载配置文件失败")
	}
	var conf Config
	_, err = toml.Decode(string(content), &conf)
	if err != nil {
		fmt.Println("Decode error", err)
		return nil, errors.New("解析配置文件失败")
	}
	return &conf, nil
}

func WriteFile(v any) error {
	buf := bytes.NewBuffer(nil)
	enc := toml.NewEncoder(buf)
	err := enc.Encode(v)
	if err != nil {
		fmt.Println("config encode fail", err)
		return err
	}
	err = os.WriteFile(configPath, buf.Bytes(), 0644)
	if err != nil {
		fmt.Println("config writeFile fail", err)
		return err
	}
	return nil
}
