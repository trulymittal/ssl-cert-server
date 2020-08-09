package main

import (
	"context"
	"flag"
	"io/ioutil"
	"log"
	"regexp"
	"strings"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"gopkg.in/yaml.v2"
)

// StringArray implements flag.Value interface.
type StringArray []string

func (v *StringArray) Set(s string) error {
	*v = append(*v, s)
	return nil
}

func (v *StringArray) String() string {
	return strings.Join(*v, ",")
}

type config struct {
	Listen  string `yaml:"listen"`   // default: "127.0.0.1:8999"
	PIDFile string `yaml:"pid_file"` // default: "ssl-cert-server.pid"

	Storage struct {
		Type     string `yaml:"type"`      // dir_cache | redis, default: dir_cache
		DirCache string `yaml:"dir_cache"` // default: "./secret-dir"
		Redis    struct {
			Addr string `yaml:"addr"` // default: "127.0.0.1:6379"
		} `yaml:"redis"`

		// Cache is used by Manager to store and retrieve previously obtained certificates
		// and other account data as opaque blobs.
		Cache autocert.Cache `yaml:"-"`
	} `yaml:"storage"`

	Managed []struct {
		Pattern string `yaml:"pattern"`
		Cert    string `yaml:"cert"`
		PrivKey string `yaml:"priv_key"`

		Regex *regexp.Regexp `yaml:"-"`
	} `yaml:"managed"`

	LetsEncrypt struct {
		Staging     bool     `yaml:"staging"`      // default: false
		ForceRSA    bool     `yaml:"force_rsa"`    // default: false
		RenewBefore int      `yaml:"renew_before"` // default: 30
		Email       string   `yaml:"email"`
		Domains     []string `yaml:"domains"`
		REPatterns  []string `yaml:"re_patterns"`

		// HostPolicy is built from DomainList and PatternList.
		HostPolicy autocert.HostPolicy `yaml:"-"`

		// DirectoryURL will be set to staging api if option Staging is true,
		// else it will be Let's Encrypt production api.
		DirectoryURL string `yaml:"-"`
	} `yaml:"lets_encrypt"`

	SelfSigned struct {
		Enable       bool     `yaml:"enable"`       // default: false
		ValidDays    int      `yaml:"valid_days"`   // default: 365
		Organization []string `yaml:"organization"` // default: ["SSL Cert Server Self-Signed"]
		Cert         string   `yaml:"cert"`         // default: "self_signed.cert"
		PrivKey      string   `yaml:"priv_key"`     // default: "self_signed.key"
	} `yaml:"self_signed"`
}

func (p *config) setupDefaultOptions() {
	if Cfg.Listen == "" {
		Cfg.Listen = "127.0.0.1:8999"
	}
	if Cfg.PIDFile == "" {
		Cfg.Listen = "ssl-cert-server.pid"
	}

	if Cfg.Storage.Type == "" {
		Cfg.Storage.Type = "dir_cache"
	}
	if Cfg.Storage.DirCache == "" {
		Cfg.Storage.DirCache = "./secret-dir"
	}
	if Cfg.Storage.Redis.Addr == "" {
		Cfg.Storage.Redis.Addr = "127.0.0.1:6379"
	}

	if Cfg.LetsEncrypt.RenewBefore <= 0 {
		Cfg.LetsEncrypt.RenewBefore = 30
	}
	if Cfg.LetsEncrypt.Staging {
		Cfg.LetsEncrypt.DirectoryURL = stagingDirectoryURL
	} else {
		Cfg.LetsEncrypt.DirectoryURL = acme.LetsEncryptURL
	}

	if Cfg.SelfSigned.ValidDays <= 0 {
		Cfg.SelfSigned.ValidDays = 365
	}
	if len(Cfg.SelfSigned.Organization) == 0 {
		Cfg.SelfSigned.Organization = defaultSelfSignedOrganization
	}
	if Cfg.SelfSigned.Cert == "" {
		Cfg.SelfSigned.Cert = "self_signed.cert"
	}
	if Cfg.SelfSigned.PrivKey == "" {
		Cfg.SelfSigned.PrivKey = "self_signed.key"
	}
}

func (p *config) buildHostPolicy() {
	var listPolicy autocert.HostPolicy
	var rePolicy autocert.HostPolicy
	if len(p.LetsEncrypt.Domains) > 0 {
		listPolicy = HostWhitelist(p.LetsEncrypt.Domains...)
	}
	if len(p.LetsEncrypt.REPatterns) > 0 {
		patterns := make([]*regexp.Regexp, len(p.LetsEncrypt.REPatterns))
		for i, p := range p.LetsEncrypt.REPatterns {
			re, err := regexp.Compile(p)
			if err != nil {
				log.Fatalf("[FATAL] server: failed compile lets_encrypte domain pattern: %q, %v", p, err)
			}
			patterns[i] = re
		}
		rePolicy = RegexpWhitelist(patterns...)
	}

	// no domains specified, allow any domain by default
	if listPolicy == nil && rePolicy == nil {
		p.LetsEncrypt.HostPolicy = func(ctx context.Context, host string) error {
			return nil
		}
		return
	}

	// first check plain domain list
	// then check regex domain patterns
	p.LetsEncrypt.HostPolicy = func(ctx context.Context, host string) (err error) {
		if listPolicy != nil {
			if err = listPolicy(ctx, host); err == nil {
				return nil
			}
		}
		if rePolicy != nil {
			if err = rePolicy(ctx, host); err != nil {
				return nil
			}
		}
		return err
	}
}

var Cfg = &config{}

var Flags struct {
	ShowVersion bool   // default: false
	ConfigFile  string // default: "./conf.yaml"
}

func initFlags() {
	flag.BoolVar(&Flags.ShowVersion, "version", false, "print version string and quit")
	flag.StringVar(&Flags.ConfigFile, "config", "./conf.yaml", "configuration filename")
	flag.Parse()
}

func initConfig() {
	confbuf, err := ioutil.ReadFile(Flags.ConfigFile)
	if err != nil {
		log.Fatalf("[FATAL] server: failed read configuration: %v", err)
	}
	err = yaml.UnmarshalStrict(confbuf, Cfg)
	if err != nil {
		log.Fatalf("[FATAL] server: failed read configuration: %v", err)
	}

	// Prepare configuration.

	Cfg.setupDefaultOptions()
	Cfg.buildHostPolicy()

	switch Cfg.Storage.Type {
	case "dir_cache":
		Cfg.Storage.Cache, _ = NewDirCache(Cfg.Storage.DirCache)
	case "redis":
		Cfg.Storage.Cache, err = NewRedisCache(Cfg.Storage.Redis.Addr)
		if err != nil {
			log.Fatalf("[FATAL] server: failed setup redis storage: %v", err)
		}
	}

	for i := range Cfg.Managed {
		pattern := Cfg.Managed[i].Pattern
		re, err := regexp.Compile(pattern)
		if err != nil {
			log.Fatalf("[FATAL] server: failed compile managed domain pattern: %q, %v", pattern, err)
		}
		Cfg.Managed[i].Regex = re
	}
}
