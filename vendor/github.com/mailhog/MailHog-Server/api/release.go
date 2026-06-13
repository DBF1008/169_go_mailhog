package api

import (
	"fmt"
	"net/smtp"

	"github.com/ian-kent/go-log/log"
	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/data"
)

// releaseViaSMTP sends a message using the given SMTP configuration.
// This is shared between v1 and v2 release handlers.
func releaseViaSMTP(msg *data.Message, cfg *config.OutgoingSMTP, hostname string) error {
	log.Printf("Releasing to %s (via %s:%s)", cfg.Email, cfg.Host, cfg.Port)

	bytes := make([]byte, 0)
	for h, l := range msg.Content.Headers {
		for _, v := range l {
			bytes = append(bytes, []byte(h+": "+v+"\r\n")...)
		}
	}
	bytes = append(bytes, []byte("\r\n"+msg.Content.Body)...)

	var auth smtp.Auth

	if len(cfg.Username) > 0 || len(cfg.Password) > 0 {
		log.Printf("Found username/password, using auth mechanism: [%s]", cfg.Mechanism)
		switch cfg.Mechanism {
		case "CRAMMD5":
			auth = smtp.CRAMMD5Auth(cfg.Username, cfg.Password)
		case "PLAIN":
			auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		default:
			return fmt.Errorf("invalid authentication mechanism: %s", cfg.Mechanism)
		}
	}

	return smtp.SendMail(cfg.Host+":"+cfg.Port, auth, "nobody@"+hostname, []string{cfg.Email}, bytes)
}
