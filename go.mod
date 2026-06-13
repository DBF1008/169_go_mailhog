module github.com/mailhog/MailHog

go 1.26

require (
	github.com/gorilla/pat v0.0.0
	github.com/ian-kent/envconf v0.0.0
	github.com/ian-kent/go-log v0.0.0
	github.com/ian-kent/goose v0.0.0
	github.com/jtolds/gls v0.0.0
	github.com/mailhog/MailHog-Server v0.0.0
	github.com/mailhog/MailHog-UI v0.0.0
	github.com/mailhog/data v0.0.0
	github.com/mailhog/http v0.0.0
	github.com/mailhog/mhsendmail v0.0.0
	github.com/mailhog/smtp v0.0.0
	github.com/mailhog/storage v0.0.0
	github.com/philhofer/fwd v0.0.0
	github.com/smartystreets/goconvey v0.0.0
	github.com/spf13/cobra v0.0.0
	github.com/spf13/pflag v0.0.0
	github.com/t-k/fluent-logger-golang v0.0.0
	github.com/tinylib/msgp v0.0.0
	golang.org/x/crypto v0.0.0
	gopkg.in/mgo.v2 v2.0.0
)

replace (
	github.com/gorilla/pat => ./vendor/github.com/gorilla/pat
	github.com/ian-kent/envconf => ./vendor/github.com/ian-kent/envconf
	github.com/ian-kent/go-log => ./vendor/github.com/ian-kent/go-log
	github.com/ian-kent/goose => ./vendor/github.com/ian-kent/goose
	github.com/jtolds/gls => ./vendor/github.com/jtolds/gls
	github.com/mailhog/MailHog-Server => ./vendor/github.com/mailhog/MailHog-Server
	github.com/mailhog/MailHog-UI => ./vendor/github.com/mailhog/MailHog-UI
	github.com/mailhog/data => ./vendor/github.com/mailhog/data
	github.com/mailhog/http => ./vendor/github.com/mailhog/http
	github.com/mailhog/mhsendmail => ./vendor/github.com/mailhog/mhsendmail
	github.com/mailhog/smtp => ./vendor/github.com/mailhog/smtp
	github.com/mailhog/storage => ./vendor/github.com/mailhog/storage
	github.com/philhofer/fwd => ./vendor/github.com/philhofer/fwd
	github.com/smartystreets/goconvey => ./vendor/github.com/smartystreets/goconvey
	github.com/spf13/cobra => ./vendor/github.com/spf13/cobra
	github.com/spf13/pflag => ./vendor/github.com/spf13/pflag
	github.com/t-k/fluent-logger-golang => ./vendor/github.com/t-k/fluent-logger-golang
	github.com/tinylib/msgp => ./vendor/github.com/tinylib/msgp
	golang.org/x/crypto => ./vendor/golang.org/x/crypto
	gopkg.in/mgo.v2 => ./vendor/gopkg.in/mgo.v2
)
