// Package config reads famulus.toml: whose word counts, which repositories to watch and
// which agent works on them.
package config

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/BurntSushi/toml"
)

// Agent names the runner knows.
const (
	Claude   = "claude"
	Codex    = "codex"
	OpenCode = "opencode"
)

type Config struct {
	// Owner is the GitHub login whose labels and comments the runner obeys.
	Owner    string        `toml:"owner"`
	Label    string        `toml:"label"`
	Interval time.Duration `toml:"interval"`
	Timeout  time.Duration `toml:"timeout"`
	// LimitPause is how long an agent rests after its plan ran out, when the provider gave
	// no time of its own.
	LimitPause time.Duration `toml:"limit_pause"`
	// DailyLimit caps new issues started per UTC day; 0 turns the cap off.
	DailyLimit int    `toml:"daily_limit"`
	WorkDir    string `toml:"work_dir"`
	// Panel is the listen address of the admin page; empty turns it off.
	Panel  string            `toml:"panel"`
	Agent  string            `toml:"agent"`
	Models map[string]string `toml:"models"`
	// Effort is the reasoning effort per agent; only the claude CLI takes it as a flag.
	Effort map[string]string `toml:"effort"`
	Repos  []Repo            `toml:"repos"`
}

type Repo struct {
	Name  string `toml:"name"`
	Base  string `toml:"base"`
	Agent string `toml:"agent"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		Label:      "agent",
		Interval:   time.Minute,
		Timeout:    time.Hour,
		LimitPause: time.Hour,
		DailyLimit: 5,
		WorkDir:    "/work",
		Panel:      ":8080",
		Agent:      OpenCode,
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	return cfg, cfg.check()
}

func (c Config) check() error {
	if c.Owner == "" {
		return errors.New("owner is empty")
	}
	if len(c.Repos) == 0 {
		return errors.New("no repos")
	}
	if c.DailyLimit < 0 {
		return errors.New("daily_limit is negative")
	}
	if err := checkAgent(c.Agent); err != nil {
		return err
	}
	for name, level := range c.Effort {
		if name != Claude {
			return fmt.Errorf("effort is for claude only, got %q", name)
		}
		if !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, level) {
			return fmt.Errorf("effort %q: want low, medium, high, xhigh or max", level)
		}
	}
	for _, r := range c.Repos {
		if r.Name == "" || r.Base == "" {
			return fmt.Errorf("repo %q: name and base are required", r.Name)
		}
		if r.Agent != "" {
			if err := checkAgent(r.Agent); err != nil {
				return fmt.Errorf("repo %s: %w", r.Name, err)
			}
		}
	}
	return nil
}

func checkAgent(name string) error {
	switch name {
	case Claude, Codex, OpenCode:
		return nil
	}
	return fmt.Errorf("unknown agent %q: want claude, codex or opencode", name)
}
