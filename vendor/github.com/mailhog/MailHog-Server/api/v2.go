package api

import (
	"encoding/json"
	"net/http"
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

	r.Path(conf.WebPath + "/api/v2/outgoing-smtp/{name}").Methods("PUT").HandlerFunc(apiv2.updateOutgoingSMTP)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp/{name}").Methods("DELETE").HandlerFunc(apiv2.deleteOutgoingSMTP)
	r.Path(conf.WebPath + "/api/v2/outgoing-smtp/{name}").Methods("OPTIONS").HandlerFunc(apiv2.defaultOptions)

	r.Path(conf.WebPath + "/api/v2/messages/{id}/release").Methods("POST").HandlerFunc(apiv2.releaseMessage)
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

// respondJSON writes a JSON response with the given status code.
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
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

	apiv2.config.OutgoingSMTPMu.Lock()
	defer apiv2.config.OutgoingSMTPMu.Unlock()

	b, _ := json.Marshal(apiv2.config.OutgoingSMTP)
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func (apiv2 *APIv2) createOutgoingSMTP(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] POST /api/v2/outgoing-smtp")

	apiv2.defaultOptions(w, req)

	var smtpCfg config.OutgoingSMTP
	if err := json.NewDecoder(req.Body).Decode(&smtpCfg); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	if err := smtpCfg.ValidateForCreate(); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	apiv2.config.OutgoingSMTPMu.Lock()
	defer apiv2.config.OutgoingSMTPMu.Unlock()

	if _, exists := apiv2.config.OutgoingSMTP[smtpCfg.Name]; exists {
		respondJSON(w, http.StatusConflict, map[string]string{"error": "template '" + smtpCfg.Name + "' already exists"})
		return
	}

	smtpCfg.Save = false
	apiv2.config.OutgoingSMTP[smtpCfg.Name] = &smtpCfg
	respondJSON(w, http.StatusCreated, smtpCfg)
}

func (apiv2 *APIv2) updateOutgoingSMTP(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] PUT /api/v2/outgoing-smtp/{name}")

	apiv2.defaultOptions(w, req)

	name := req.URL.Query().Get(":name")
	if name == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "name parameter is required"})
		return
	}

	var incoming config.OutgoingSMTP
	if err := json.NewDecoder(req.Body).Decode(&incoming); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	if incoming.Mechanism != "" && incoming.Mechanism != "PLAIN" && incoming.Mechanism != "CRAMMD5" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "mechanism must be PLAIN or CRAMMD5"})
		return
	}

	apiv2.config.OutgoingSMTPMu.Lock()
	defer apiv2.config.OutgoingSMTPMu.Unlock()

	existing, exists := apiv2.config.OutgoingSMTP[name]
	if !exists {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "template '" + name + "' not found"})
		return
	}

	// Partial update: only overwrite non-zero fields from incoming
	if incoming.Host != "" {
		existing.Host = incoming.Host
	}
	if incoming.Port != "" {
		existing.Port = incoming.Port
	}
	if incoming.Email != "" {
		existing.Email = incoming.Email
	}
	if incoming.Username != "" {
		existing.Username = incoming.Username
	}
	if incoming.Password != "" {
		existing.Password = incoming.Password
	}
	if incoming.Mechanism != "" {
		existing.Mechanism = incoming.Mechanism
	}

	respondJSON(w, http.StatusOK, existing)
}

func (apiv2 *APIv2) deleteOutgoingSMTP(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] DELETE /api/v2/outgoing-smtp/{name}")

	apiv2.defaultOptions(w, req)

	name := req.URL.Query().Get(":name")
	if name == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "name parameter is required"})
		return
	}

	apiv2.config.OutgoingSMTPMu.Lock()
	defer apiv2.config.OutgoingSMTPMu.Unlock()

	if _, exists := apiv2.config.OutgoingSMTP[name]; !exists {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "template '" + name + "' not found"})
		return
	}

	delete(apiv2.config.OutgoingSMTP, name)
	w.WriteHeader(http.StatusNoContent)
}

// ReleaseRequest is the request body for releasing a message via a saved template or inline config.
type ReleaseRequest struct {
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	Host      string `json:"host,omitempty"`
	Port      string `json:"port,omitempty"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	Mechanism string `json:"mechanism,omitempty"`
}

func (apiv2 *APIv2) releaseMessage(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] POST /api/v2/messages/{id}/release")

	apiv2.defaultOptions(w, req)

	id := req.URL.Query().Get(":id")
	if id == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "message id is required"})
		return
	}

	var releaseReq ReleaseRequest
	if err := json.NewDecoder(req.Body).Decode(&releaseReq); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	// Load message from storage
	msg, err := apiv2.config.Storage.Load(id)
	if err != nil || msg == nil {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "message not found"})
		return
	}

	// Resolve SMTP config: template reference or inline
	var smtpConfig *config.OutgoingSMTP

	if releaseReq.Name != "" {
		apiv2.config.OutgoingSMTPMu.Lock()
		tmpl, exists := apiv2.config.OutgoingSMTP[releaseReq.Name]
		if !exists {
			apiv2.config.OutgoingSMTPMu.Unlock()
			respondJSON(w, http.StatusNotFound, map[string]string{"error": "template '" + releaseReq.Name + "' not found"})
			return
		}
		// Copy to avoid holding lock during network I/O
		copied := *tmpl
		apiv2.config.OutgoingSMTPMu.Unlock()
		smtpConfig = &copied
	} else {
		smtpConfig = &config.OutgoingSMTP{
			Host:      releaseReq.Host,
			Port:      releaseReq.Port,
			Username:  releaseReq.Username,
			Password:  releaseReq.Password,
			Mechanism: releaseReq.Mechanism,
			Email:     releaseReq.Email,
		}
	}

	// Explicit email override
	if releaseReq.Email != "" {
		smtpConfig.Email = releaseReq.Email
	}

	// Validate resolved config
	if smtpConfig.Host == "" || smtpConfig.Port == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "host and port are required (provide inline or via template)"})
		return
	}
	if smtpConfig.Email == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "recipient email is required"})
		return
	}
	if smtpConfig.Mechanism != "" && smtpConfig.Mechanism != "PLAIN" && smtpConfig.Mechanism != "CRAMMD5" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "mechanism must be PLAIN or CRAMMD5"})
		return
	}

	// Send via shared SMTP function
	if err := releaseViaSMTP(msg, smtpConfig, apiv2.config.Hostname); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "release failed: " + err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

func (apiv2 *APIv2) websocket(w http.ResponseWriter, req *http.Request) {
	log.Println("[APIv2] GET /api/v2/websocket")

	apiv2.wsHub.Serve(w, req)
}

func (apiv2 *APIv2) broadcast(msg *data.Message) {
	log.Println("[APIv2] BROADCAST /api/v2/websocket")

	apiv2.wsHub.Broadcast(msg)
}
