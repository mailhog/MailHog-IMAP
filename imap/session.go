package imap

// https://www.ietf.org/rfc/rfc3501.txt

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/mailhog/backends/auth"
	"github.com/mailhog/imap"
)

// Session represents a SMTP session using net.TCPConn
type Session struct {
	server *Server

	conn          io.ReadWriteCloser
	proto         *imap.Protocol
	remoteAddress string
	isTLS         bool
	line          string
	identity      auth.Identity
	responseChan  chan *imap.Response

	maximumBufferLength int
}

// Accept starts a new SMTP session using io.ReadWriteCloser
func (s *Server) Accept(remoteAddress string, conn io.ReadWriteCloser) {
	responseChan := make(chan *imap.Response)
	proto := imap.NewProtocol(responseChan)
	proto.Hostname = s.Hostname

	session := &Session{
		server:              s,
		conn:                conn,
		proto:               proto,
		remoteAddress:       remoteAddress,
		isTLS:               false,
		line:                "",
		identity:            nil,
		maximumBufferLength: 2048000,
		responseChan:        responseChan,
	}

	// FIXME this all feels nasty
	proto.LogHandler = session.logf
	proto.ValidateAuthenticationHandler = session.validateAuthentication
	if session.server != nil && session.server.AuthBackend != nil {
		proto.GetAuthenticationMechanismsHandler = session.server.AuthBackend.Mechanisms
	}

	if !session.server.PolicySet.DisableTLS {
		session.logf("Enabling TLS support")
		proto.TLSHandler = session.tlsHandler
		proto.RequireTLS = session.server.PolicySet.RequireTLS
	}

	session.logf("Starting session")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for r := range proto.Responses {
			session.Write(r)
		}
	}()
	session.Write(proto.Start())
	for session.Read() == true {
	}
	wg.Wait()
	io.Closer(conn).Close()
	session.logf("Session ended")
}

func (c *Session) validateAuthentication(mechanism string, args ...string) (ok bool) {
	if c.server.AuthBackend == nil {
		// FIXME
		//c.responseChan <- imap.ReplyInvalidAuth()
		return false
	}
	i, e, ok := c.server.AuthBackend.Authenticate(mechanism, args...)
	if e != nil || !ok {
		if e != nil {
			c.logf("error authenticating: %s", e)
		}
		// FIXME
		//c.responseChan <- imap.ReplyInvalidAuth()
		return false
	}
	c.identity = i
	return true
}

// tlsHandler handles the STARTTLS command
func (c *Session) tlsHandler(done func(ok bool)) (errorReply *imap.Response, callback func(), ok bool) {
	c.logf("Returning TLS handler")
	return nil, func() {
		c.logf("Upgrading session to TLS")
		// FIXME errors reading TLS config? should preload it
		tConn := tls.Server(c.conn.(net.Conn), c.server.getTLSConfig())
		err := tConn.Handshake()
		c.conn = tConn
		if err != nil {
			c.logf("handshake error in TLS connection: %s", err)
			done(false)
			return
		}
		c.isTLS = true
		c.logf("Session upgrade complete")
		done(true)
	}, true
}

func (c *Session) logf(message string, args ...interface{}) {
	message = strings.Join([]string{"[SMTP %s]", message}, " ")
	args = append([]interface{}{c.remoteAddress}, args...)
	log.Printf(message, args...)
}

// Read reads from the underlying io.Reader
func (c *Session) Read() bool {
	if c.proto.TLSPending && !c.proto.TLSUpgraded {
		// this avoids reading from the socket during TLS negotation
		// (differs from mailhog/MailHog-MTA since we use asynchronous writes via c.responseChan)
		// XXX is there a race condition here in setting c.proto.TLSPending from TLSHandler?
		return true
	}
	buf := make([]byte, 1024)
	n, err := io.Reader(c.conn).Read(buf)

	if n == 0 {
		c.logf("Connection closed by remote host\n")
		io.Closer(c.conn).Close() // not sure this is necessary?
		return false
	}

	if err != nil {
		c.logf("Error reading from socket: %s\n", err)
		return false
	}

	text := string(buf[0:n])
	logText := strings.Replace(text, "\n", "\\n", -1)
	logText = strings.Replace(logText, "\r", "\\r", -1)
	c.logf("Received %d bytes: '%s'\n", n, logText)

	if c.maximumBufferLength > -1 && len(c.line+text) > c.maximumBufferLength {
		// FIXME what is the "expected" behaviour for this?
		// FIXME and should this be tagged?
		c.Write(imap.ResponseError("*", fmt.Errorf("Maximum buffer length exceeded")))
		return false
	}

	c.line += text

	for strings.Contains(c.line, "\r\n") {
		line := c.proto.Parse(c.line)
		c.line = line

		// FIXME need to detect connection closed?
	}

	return true
}

// Write writes a reply to the underlying io.Writer
func (c *Session) Write(reply *imap.Response) {
	lines := reply.Lines()
	for _, l := range lines {
		logText := strings.Replace(l, "\n", "\\n", -1)
		logText = strings.Replace(logText, "\r", "\\r", -1)
		c.logf("Sent %d bytes: '%s'", len(l), logText)
		io.Writer(c.conn).Write([]byte(l))
	}
	if reply.Done != nil {
		reply.Done()
	}
}
