package main

import (
	"flag"
	"log"
	"sync"

	"github.com/mailhog/MailHog-IMAP/config"
	"github.com/mailhog/MailHog-IMAP/imap"
	"github.com/mailhog/backends/auth"
	sconfig "github.com/mailhog/backends/config"
	"github.com/mailhog/backends/mailbox"
)

var conf *config.Config
var wg sync.WaitGroup

func configure() {
	config.RegisterFlags()
	flag.Parse()
	conf = config.Configure()
}

func main() {
	configure()

	for _, s := range conf.Servers {
		wg.Add(1)
		go func(s *config.Server) {
			defer wg.Done()
			err := newServer(conf, s)
			if err != nil {
				log.Fatal(err)
			}
		}(s)
	}

	wg.Wait()
}

func newServer(cfg *config.Config, server *config.Server) error {
	var a, d, r sconfig.BackendConfig
	var err error

	if server.Backends.Auth != nil {
		a, err = server.Backends.Auth.Resolve(cfg.Backends)
		if err != nil {
			return err
		}
	}
	if server.Backends.Mailbox != nil {
		r, err = server.Backends.Mailbox.Resolve(cfg.Backends)
		if err != nil {
			return err
		}
	}

	s := &imap.Server{
		BindAddr:       server.BindAddr,
		Hostname:       server.Hostname,
		PolicySet:      server.PolicySet,
		AuthBackend:    auth.Load(a, *cfg),
		MailboxBackend: mailbox.Load(m, *cfg),
		Config:         cfg,
		Server:         server,
	}

	return s.Listen()
}
