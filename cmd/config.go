package main

import (
	"strings"

	"github.com/spf13/viper"
)

type fileConfig struct {
	Temporal *temporalConfig `mapstructure:"temporal"`
	LLM     *llmConfig       `mapstructure:"llm"`
}

type temporalConfig struct {
	Host             string `mapstructure:"host"`
	Port             int    `mapstructure:"port"`
	Namespace        string `mapstructure:"namespace"`
	TaskQueue string `mapstructure:"taskQueue"`
}

type llmConfig struct {
	Type    string `mapstructure:"type"`
	APIKey  string `mapstructure:"apiKey"`
	Model   string `mapstructure:"model"`
	BaseURL string `mapstructure:"baseURL"`
}

func loadConfigFromFile(path string) (*fileConfig, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}
	var cfg fileConfig
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
