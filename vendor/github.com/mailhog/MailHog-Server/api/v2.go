package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/smtp"
	"strconv"

	"github.com/gorilla/pat"
	"github.com/ian-kent/go-log/log"
	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/MailHog-Server/monkey"
	"github.com/mailhog/MailHog-Server/websockets"
	"github.com/mailhog/data"
)

// APIv2 implements version 2 of the MailHog API
//
// It is currently experimental and may change in future releases.
// Use APIv1 for guaranteed compatibility.
type APIv2 struct {
	config      *config.Config
	messageChan chan *data.Message
	wsHub       *websockets.Hub
}

func createAPIv2(conf *config.Config, r *pat.Router) *APIv2 {
	log.Println("Creating API v2 with WebPath: " + conf.WebPath)
	apiv2 := &APIv2{
		config:      conf,
		messageChan: make(chan *data.Message),
		wsHub:       websockets.NewHub(),
	}

	r.Path(conf.WebPath + "/api/v2/messages").Methods("GET").HandlerFunc(apiv2.messages)
	r.Path(conf.WebPath + "/api/v2/messages").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/search").Methods("GET").HandlerFunc(apiv2.search)
	r.Path(conf.WebPath + "/api/v2/search").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/jim").Methods("GET").HandlerFunc(apiv2.jim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("POST").HandlerFunc(apiv2.createJim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("PUT").HandlerFunc(apiv2.updateJim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("DELETE").HandlerFunc(apiv2.deleteJim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/outgoing-smtp").Methods("GET").HandlerFunc(apiv2.listOutgoingSMTP)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp").Methods("POST").HandlerFunc(apiv2.createOutgoingSMTP)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/outgoing-smtp/{name}").Methods("GET").HandlerFunc(apiv2.getOutgoingSMTP)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp/{name}").Methods("PUT").HandlerFunc(apiv2.updateOutgoingSMTP)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp/{name}").Methods("DELETE").HandlerFunc(apiv2.deleteOutgoingSMTP)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp/{name}").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/messages/{id}/release").Methods("POST").HandlerFunc(apiv2.releaseOne)
	r.Path(conf.WebPath + "/api/v2/messages/{id}/release").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/websocket").Methods("GET").HandlerFunc(apiv2.websocket)

	go func() {
		for {
			select {
			case msg := <-apiv2.messageChan:
				log.Println("Got message in APIv2 websocket channel")
				apiv2.broadcast(msg)
			}
		}
	}()

	return apiv2
}

func (apiv2 *APIv2) defaultOptions(w http.ResponseWriter, req *http.Request) {
	if len(apiv2.config.CORSOrigin) > 0 {
		w.Header().Add("Access-Control-Allow-Origin", apiv2.config.CORSOrigin)
		w.Header().Add("Access-Control-Allow-Methods", "OPTIONS,GET,PUT,POST,DELETE")
		w.Header().Add("Access-Control-Allow-Headers", "Content-Type")
	}
}

type messagesResult struct {
	Total int            `json:"total"`
	Count int            `json:"count"`
	Start int            `json:"start"`
	Items []data.Message `json:"items"`
}

func (apiv2 *APIv2) getStartLimit(w http.ResponseWriter, req *http.Request) (start, limit int) {
	start = 0
	limit = 50

	s := req.URL.Query().Get("start")
	if n, e := strconv.ParseInt(s, 10, 64); e == nil && n > 0 {
		start = int(n)
	}

	l := req.URL.Query().Get("limit")
	if n, e := strconv.ParseInt(l, 10, 64); e == nil && n > 0 {
		if n > 250 {
			n = 250
		}
		limit = int(n)
	}

	return
}

func (apiv2 *APIv2) messages(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/messages")

	apiv2.defaultOptions(w, req)

	start, limit := apiv2.getStartLimit(w, req)

	var res messagesResult

	messages, err := apiv2.config.Storage.List(start, limit)
	if err != nil {
		panic(err)
	}

	res.Count = len([]data.Message(*messages))
	res.Start = start
	res.Items = []data.Message(*messages)
	res.Total = apiv2.config.Storage.Count()

	bytes, _ := json.Marshal(res)
	w.Header().Add("Content-Type", "text/json")
	w.Write(bytes)
}

func (apiv2 *APIv2) search(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/search")

	apiv2.defaultOptions(w, req)

	start, limit := apiv2.getStartLimit(w, req)

	kind := req.URL.Query().Get("kind")
	if kind != "from" && kind != "to" && kind != "containing" {
		w.WriteHeader(400)
		return
	}

	query := req.URL.Query().Get("query")
	if len(query) == 0 {
		w.WriteHeader(400)
		return
	}

	var res messagesResult

	messages, total, _ := apiv2.config.Storage.Search(kind, query, start, limit)

	res.Count = len([]data.Message(*messages))
	res.Start = start
	res.Items = []data.Message(*messages)
	res.Total = total

	b, _ := json.Marshal(res)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) jim(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/jim")

	apiv2.defaultOptions(w, req)

	if apiv2.config.Monkey == nil {
		w.WriteHeader(404)
		return
	}

	b, _ := json.Marshal(apiv2.config.Monkey)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) deleteJim(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] DELETE /api/v2/jim")

	apiv2.defaultOptions(w, req)

	if apiv2.config.Monkey == nil {
		w.WriteHeader(404)
		return
	}

	apiv2.config.Monkey = nil
}

func (apiv2 *APIv2) createJim(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] POST /api/v2/jim")

	apiv2.defaultOptions(w, req)

	if apiv2.config.Monkey != nil {
		w.WriteHeader(400)
		return
	}

	apiv2.config.Monkey = config.Jim

	// Try, but ignore errors
	// Could be better (e.g., ok if no json, error if badly formed json)
	// but this works for now
	apiv2.newJimFromBody(w, req)

	w.WriteHeader(201)
}

func (apiv2 *APIv2) newJimFromBody(w http.ResponseWriter, req *http.Request) error {
	var jim monkey.Jim

	dec := json.NewDecoder(req.Body)
	err := dec.Decode(&jim)

	if err != nil {
		return err
	}

	jim.ConfigureFrom(config.Jim)

	config.Jim = &jim
	apiv2.config.Monkey = &jim

	return nil
}

func (apiv2 *APIv2) updateJim(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] PUT /api/v2/jim")

	apiv2.defaultOptions(w, req)

	if apiv2.config.Monkey == nil {
		w.WriteHeader(404)
		return
	}

	err := apiv2.newJimFromBody(w, req)
	if err != nil {
		w.WriteHeader(400)
	}
}

func (apiv2 *APIv2) listOutgoingSMTP(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/outgoing-smtp")

	apiv2.defaultOptions(w, req)

	b, _ := json.Marshal(apiv2.config.OutgoingSMTP)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

// smtpSendMail sends a message via SMTP. It is a package-level variable so
// tests can substitute it without performing real network I/O.
var smtpSendMail = smtp.SendMail

// validateOutgoingSMTP checks that an outgoing SMTP server template is usable.
func validateOutgoingSMTP(s *config.OutgoingSMTP) error {
	if len(s.Name) == 0 {
		return errors.New("name is required")
	}
	if len(s.Host) == 0 {
		return errors.New("host is required")
	}
	if len(s.Port) == 0 {
		return errors.New("port is required")
	}
	switch s.Mechanism {
	case "", "PLAIN", "CRAMMD5":
		// supported
	default:
		return errors.New("mechanism must be PLAIN or CRAMMD5")
	}
	if (len(s.Username) > 0 || len(s.Password) > 0) && s.Mechanism != "PLAIN" && s.Mechanism != "CRAMMD5" {
		return errors.New("mechanism must be PLAIN or CRAMMD5 when a username or password is provided")
	}
	return nil
}

func (apiv2 *APIv2) getOutgoingSMTP(w http.ResponseWriter, req *http.Request) {
	name := req.URL.Query().Get(":name")
	log.Printf("[APIv2] GET /api/v2/outgoing-smtp/%s\n", name)

	apiv2.defaultOptions(w, req)

	server, ok := apiv2.config.OutgoingSMTP[name]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	b, _ := json.Marshal(server)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) createOutgoingSMTP(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] POST /api/v2/outgoing-smtp")

	apiv2.defaultOptions(w, req)

	var server config.OutgoingSMTP
	if err := json.NewDecoder(req.Body).Decode(&server); err != nil {
		log.Printf("Error decoding request body: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Error decoding request body"))
		return
	}

	if err := validateOutgoingSMTP(&server); err != nil {
		log.Printf("Invalid outgoing SMTP server: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}

	if apiv2.config.OutgoingSMTP == nil {
		apiv2.config.OutgoingSMTP = make(map[string]*config.OutgoingSMTP)
	}

	if _, ok := apiv2.config.OutgoingSMTP[server.Name]; ok {
		log.Printf("Outgoing SMTP server already exists: %s", server.Name)
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte("An outgoing SMTP server with that name already exists"))
		return
	}

	apiv2.config.OutgoingSMTP[server.Name] = &server
	log.Printf("Created outgoing SMTP server %s", server.Name)

	b, _ := json.Marshal(server)
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(b)
}

func (apiv2 *APIv2) updateOutgoingSMTP(w http.ResponseWriter, req *http.Request) {
	name := req.URL.Query().Get(":name")
	log.Printf("[APIv2] PUT /api/v2/outgoing-smtp/%s\n", name)

	apiv2.defaultOptions(w, req)

	if _, ok := apiv2.config.OutgoingSMTP[name]; !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var server config.OutgoingSMTP
	if err := json.NewDecoder(req.Body).Decode(&server); err != nil {
		log.Printf("Error decoding request body: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Error decoding request body"))
		return
	}

	// The name in the path identifies the template and is authoritative.
	server.Name = name

	if err := validateOutgoingSMTP(&server); err != nil {
		log.Printf("Invalid outgoing SMTP server: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}

	apiv2.config.OutgoingSMTP[name] = &server
	log.Printf("Updated outgoing SMTP server %s", name)

	b, _ := json.Marshal(server)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) deleteOutgoingSMTP(w http.ResponseWriter, req *http.Request) {
	name := req.URL.Query().Get(":name")
	log.Printf("[APIv2] DELETE /api/v2/outgoing-smtp/%s\n", name)

	apiv2.defaultOptions(w, req)

	if _, ok := apiv2.config.OutgoingSMTP[name]; !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	delete(apiv2.config.OutgoingSMTP, name)
	log.Printf("Deleted outgoing SMTP server %s", name)
	w.WriteHeader(http.StatusOK)
}

// releaseRequest is the body of a v2 release: it names a stored outgoing SMTP
// template to reuse, and may explicitly override the recipient address.
type releaseRequest struct {
	Name  string
	Email string
}

func (apiv2 *APIv2) releaseOne(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get(":id")
	log.Printf("[APIv2] POST /api/v2/messages/%s/release\n", id)

	apiv2.defaultOptions(w, req)

	w.Header().Add("Content-Type", "text/json")

	var rel releaseRequest
	if err := json.NewDecoder(req.Body).Decode(&rel); err != nil {
		log.Printf("Error decoding request body: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Error decoding request body"))
		return
	}

	// v2 release only ever reuses a saved template; it never accepts an inline
	// server config. A template name is therefore required.
	if len(rel.Name) == 0 {
		log.Printf("No outgoing SMTP server name provided")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("An outgoing SMTP server name is required"))
		return
	}

	server, ok := apiv2.config.OutgoingSMTP[rel.Name]
	if !ok {
		log.Printf("Outgoing SMTP server not found: %s", rel.Name)
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("Outgoing SMTP server not found"))
		return
	}

	// The recipient may be overridden per-release; otherwise fall back to the
	// address stored on the template.
	email := server.Email
	if len(rel.Email) > 0 {
		email = rel.Email
	}
	if len(email) == 0 {
		log.Printf("No recipient address for release")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("A recipient email address is required"))
		return
	}

	msg, err := apiv2.config.Storage.Load(id)
	if err != nil {
		log.Printf("Error loading message %s: %s", id, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if msg == nil {
		log.Printf("Message not found: %s", id)
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("Message not found"))
		return
	}

	log.Printf("Releasing %s to %s (via %s:%s)", id, email, server.Host, server.Port)

	bytes := make([]byte, 0)
	for h, l := range msg.Content.Headers {
		for _, v := range l {
			bytes = append(bytes, []byte(h+": "+v+"\r\n")...)
		}
	}
	bytes = append(bytes, []byte("\r\n"+msg.Content.Body)...)

	var auth smtp.Auth
	if len(server.Username) > 0 || len(server.Password) > 0 {
		log.Printf("Found username/password, using auth mechanism: [%s]", server.Mechanism)
		switch server.Mechanism {
		case "CRAMMD5":
			auth = smtp.CRAMMD5Auth(server.Username, server.Password)
		case "PLAIN":
			auth = smtp.PlainAuth("", server.Username, server.Password, server.Host)
		default:
			log.Printf("Error - invalid authentication mechanism")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	err = smtpSendMail(server.Host+":"+server.Port, auth, "nobody@"+apiv2.config.Hostname, []string{email}, bytes)
	if err != nil {
		log.Printf("Failed to release message: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	log.Printf("Message released successfully")
}

func (apiv2 *APIv2) websocket(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/websocket")

	apiv2.wsHub.Serve(w, req)
}

func (apiv2 *APIv2) broadcast(msg *data.Message) {
	log.Println("[APIv2] BROADCAST /api/v2/websocket")

	apiv2.wsHub.Broadcast(msg)
}
