package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"

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
	r.Path(conf.WebPath + "/api/v2/messages").Methods("DELETE").HandlerFunc(apiv2.deleteAll)
	r.Path(conf.WebPath + "/api/v2/messages").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/messages/{id}").Methods("GET").HandlerFunc(apiv2.message)
	r.Path(conf.WebPath + "/api/v2/messages/{id}").Methods("DELETE").HandlerFunc(apiv2.deleteOne)
	r.Path(conf.WebPath + "/api/v2/messages/{id}").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/messages/{id}/download").Methods("GET").HandlerFunc(apiv2.download)
	r.Path(conf.WebPath + "/api/v2/messages/{id}/download").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/messages/{id}/mime/part/{part}/download").Methods("GET").HandlerFunc(apiv2.downloadPart)
	r.Path(conf.WebPath + "/api/v2/messages/{id}/mime/part/{part}/download").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/messages/{id}/release").Methods("POST").HandlerFunc(apiv2.releaseOne)
	r.Path(conf.WebPath + "/api/v2/messages/{id}/release").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/search").Methods("GET").HandlerFunc(apiv2.search)
	r.Path(conf.WebPath + "/api/v2/search").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/jim").Methods("GET").HandlerFunc(apiv2.jim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("POST").HandlerFunc(apiv2.createJim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("PUT").HandlerFunc(apiv2.updateJim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("DELETE").HandlerFunc(apiv2.deleteJim)
	r.Path(conf.WebPath + "/api/v2/jim").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/outgoing-smtp").Methods("GET").HandlerFunc(apiv2.listOutgoingSMTP)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

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

func (apiv2 *APIv2) websocket(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/websocket")

	apiv2.wsHub.Serve(w, req)
}

func (apiv2 *APIv2) broadcast(msg *data.Message) {
	log.Println("[APIv2] BROADCAST /api/v2/websocket")

	apiv2.wsHub.Broadcast(msg)
}

func (apiv2 *APIv2) message(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get(":id")
	log.Printf("[APIv2] GET /api/v2/messages/%s\n", id)

	apiv2.defaultOptions(w, req)

	msg, err := apiv2.config.Storage.Load(id)
	if err != nil {
		log.Printf("- Error loading message: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if msg == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	bytes, err := json.Marshal(msg)
	if err != nil {
		log.Printf("- Error marshaling message: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(bytes)
}

func (apiv2 *APIv2) deleteOne(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get(":id")
	log.Printf("[APIv2] DELETE /api/v2/messages/%s\n", id)

	apiv2.defaultOptions(w, req)

	err := apiv2.config.Storage.DeleteOne(id)
	if err != nil {
		log.Printf("- Error deleting message: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (apiv2 *APIv2) deleteAll(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] DELETE /api/v2/messages")

	apiv2.defaultOptions(w, req)

	err := apiv2.config.Storage.DeleteAll()
	if err != nil {
		log.Printf("- Error deleting all messages: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (apiv2 *APIv2) download(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get(":id")
	log.Printf("[APIv2] GET /api/v2/messages/%s/download\n", id)

	apiv2.defaultOptions(w, req)

	msg, err := apiv2.config.Storage.Load(id)
	if err != nil {
		log.Printf("- Error loading message: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if msg == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+id+".eml\"")

	for h, l := range msg.Content.Headers {
		for _, v := range l {
			w.Write([]byte(h + ": " + v + "\r\n"))
		}
	}
	w.Write([]byte("\r\n" + msg.Content.Body))
}

func (apiv2 *APIv2) downloadPart(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get(":id")
	part := req.URL.Query().Get(":part")
	log.Printf("[APIv2] GET /api/v2/messages/%s/mime/part/%s/download\n", id, part)

	apiv2.defaultOptions(w, req)

	msg, err := apiv2.config.Storage.Load(id)
	if err != nil {
		log.Printf("- Error loading message: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if msg == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	pid, err := strconv.Atoi(part)
	if err != nil || msg.MIME == nil || pid < 0 || pid >= len(msg.MIME.Parts) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=\""+id+"-part-"+part+"\"")

	contentTransferEncoding := ""
	for h, l := range msg.MIME.Parts[pid].Headers {
		for _, v := range l {
			switch strings.ToLower(h) {
			case "content-disposition":
				w.Header().Set(h, v)
			case "content-transfer-encoding":
				if contentTransferEncoding == "" {
					contentTransferEncoding = v
				}
				fallthrough
			default:
				w.Header().Add(h, v)
			}
		}
	}

	body := []byte(msg.MIME.Parts[pid].Body)
	if strings.ToLower(contentTransferEncoding) == "base64" {
		body, err = base64.StdEncoding.DecodeString(msg.MIME.Parts[pid].Body)
		if err != nil {
			log.Printf("[APIv2] Decoding base64 encoded body failed: %s", err)
		}
	}
	w.Write(body)
}

func (apiv2 *APIv2) releaseOne(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get(":id")
	log.Printf("[APIv2] POST /api/v2/messages/%s/release\n", id)

	apiv2.defaultOptions(w, req)

	msg, err := apiv2.config.Storage.Load(id)
	if err != nil {
		log.Printf("- Error loading message: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if msg == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	decoder := json.NewDecoder(req.Body)
	var cfg ReleaseConfig
	err = decoder.Decode(&cfg)
	if err != nil {
		log.Printf("Error decoding request body: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Error decoding request body"))
		return
	}

	log.Printf("%+v", cfg)
	log.Printf("Got message: %s", msg.ID)

	if cfg.Save {
		if _, ok := apiv2.config.OutgoingSMTP[cfg.Name]; ok {
			log.Printf("Server already exists named %s", cfg.Name)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cf := config.OutgoingSMTP(cfg)
		apiv2.config.OutgoingSMTP[cfg.Name] = &cf
		log.Printf("Saved server with name %s", cfg.Name)
	}

	if len(cfg.Name) > 0 {
		if c, ok := apiv2.config.OutgoingSMTP[cfg.Name]; ok {
			log.Printf("Using server with name: %s", cfg.Name)
			cfg.Name = c.Name
			if len(cfg.Email) == 0 {
				cfg.Email = c.Email
			}
			cfg.Host = c.Host
			cfg.Port = c.Port
			cfg.Username = c.Username
			cfg.Password = c.Password
			cfg.Mechanism = c.Mechanism
		} else {
			log.Printf("Server not found: %s", cfg.Name)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

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
			log.Printf("Error - invalid authentication mechanism")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	err = smtp.SendMail(cfg.Host+":"+cfg.Port, auth, "nobody@"+apiv2.config.Hostname, []string{cfg.Email}, bytes)
	if err != nil {
		log.Printf("Failed to release message: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	log.Printf("Message released successfully")
}
