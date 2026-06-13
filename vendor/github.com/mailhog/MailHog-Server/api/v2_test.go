package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/data"
	"github.com/mailhog/storage"
)

// newTestAPIv2 creates an APIv2 with a fresh config and in-memory storage for testing.
func newTestAPIv2() *APIv2 {
	cfg := config.DefaultConfig()
	cfg.Storage = storage.CreateInMemory()
	cfg.Hostname = "test.example"
	return &APIv2{
		config:      cfg,
		messageChan: make(chan *data.Message),
	}
}

// doRequest is a helper to call a handler directly with a JSON body.
func doRequest(handler http.HandlerFunc, method, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// doRequestWithParam calls a handler with a URL path parameter injected as gorilla/pat expects.
func doRequestWithParam(handler http.HandlerFunc, method, path, paramName, paramValue string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if req.URL.RawQuery == "" {
		req.URL.RawQuery = ":" + paramName + "=" + paramValue
	} else {
		req.URL.RawQuery += "&:" + paramName + "=" + paramValue
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// storeTestMessage stores a simple test message and returns its ID.
func storeTestMessage(store storage.Storage) string {
	id, _ := store.Store(&data.Message{
		ID: "test-msg",
		Content: &data.Content{
			Headers: map[string][]string{"Subject": {"Test"}},
			Body:    "body",
		},
	})
	return id
}

// --- Create tests ---

func TestCreateOutgoingSMTP_ValidTemplate(t *testing.T) {
	api := newTestAPIv2()
	body := config.OutgoingSMTP{
		Name: "test-server", Host: "smtp.example.com", Port: "587",
	}
	rr := doRequest(api.createOutgoingSMTP, "POST", "/api/v2/outgoing-smtp", body)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var result config.OutgoingSMTP
	json.Unmarshal(rr.Body.Bytes(), &result)
	if result.Name != "test-server" {
		t.Errorf("expected name 'test-server', got '%s'", result.Name)
	}
	if result.Host != "smtp.example.com" {
		t.Errorf("expected host 'smtp.example.com', got '%s'", result.Host)
	}

	api.config.OutgoingSMTPMu.Lock()
	_, exists := api.config.OutgoingSMTP["test-server"]
	api.config.OutgoingSMTPMu.Unlock()
	if !exists {
		t.Error("template should be stored in map")
	}
}

func TestCreateOutgoingSMTP_MissingName(t *testing.T) {
	api := newTestAPIv2()
	body := config.OutgoingSMTP{Host: "smtp.example.com", Port: "587"}
	rr := doRequest(api.createOutgoingSMTP, "POST", "/api/v2/outgoing-smtp", body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var errResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &errResp)
	if !strings.Contains(errResp["error"], "name") {
		t.Errorf("error should mention 'name', got: %s", errResp["error"])
	}
}

func TestCreateOutgoingSMTP_MissingHost(t *testing.T) {
	api := newTestAPIv2()
	body := config.OutgoingSMTP{Name: "test", Port: "587"}
	rr := doRequest(api.createOutgoingSMTP, "POST", "/api/v2/outgoing-smtp", body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var errResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &errResp)
	if !strings.Contains(errResp["error"], "host") {
		t.Errorf("error should mention 'host', got: %s", errResp["error"])
	}
}

func TestCreateOutgoingSMTP_MissingPort(t *testing.T) {
	api := newTestAPIv2()
	body := config.OutgoingSMTP{Name: "test", Host: "smtp.example.com"}
	rr := doRequest(api.createOutgoingSMTP, "POST", "/api/v2/outgoing-smtp", body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var errResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &errResp)
	if !strings.Contains(errResp["error"], "port") {
		t.Errorf("error should mention 'port', got: %s", errResp["error"])
	}
}

func TestCreateOutgoingSMTP_InvalidMechanism(t *testing.T) {
	api := newTestAPIv2()
	body := config.OutgoingSMTP{
		Name: "test", Host: "smtp.example.com", Port: "587", Mechanism: "INVALID",
	}
	rr := doRequest(api.createOutgoingSMTP, "POST", "/api/v2/outgoing-smtp", body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var errResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &errResp)
	if !strings.Contains(errResp["error"], "mechanism") {
		t.Errorf("error should mention 'mechanism', got: %s", errResp["error"])
	}
}

func TestCreateOutgoingSMTP_DuplicateName(t *testing.T) {
	api := newTestAPIv2()
	body := config.OutgoingSMTP{
		Name: "dup", Host: "smtp.example.com", Port: "587",
	}
	// First create
	rr1 := doRequest(api.createOutgoingSMTP, "POST", "/api/v2/outgoing-smtp", body)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first create expected 201, got %d", rr1.Code)
	}

	// Duplicate
	rr2 := doRequest(api.createOutgoingSMTP, "POST", "/api/v2/outgoing-smtp", body)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("duplicate create expected 409, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var errResp map[string]string
	json.Unmarshal(rr2.Body.Bytes(), &errResp)
	if !strings.Contains(errResp["error"], "already exists") {
		t.Errorf("error should mention 'already exists', got: %s", errResp["error"])
	}
}

func TestCreateOutgoingSMTP_InvalidJSON(t *testing.T) {
	api := newTestAPIv2()
	req := httptest.NewRequest("POST", "/api/v2/outgoing-smtp",
		bytes.NewBufferString("{invalid json"))
	rr := httptest.NewRecorder()
	http.HandlerFunc(api.createOutgoingSMTP).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// --- Update tests ---

func TestUpdateOutgoingSMTP_PartialUpdate(t *testing.T) {
	api := newTestAPIv2()
	api.config.OutgoingSMTP["existing"] = &config.OutgoingSMTP{
		Name: "existing", Host: "old.host.com", Port: "25", Email: "old@test.com",
	}

	body := config.OutgoingSMTP{Host: "new.host.com"}
	rr := doRequestWithParam(api.updateOutgoingSMTP, "PUT", "/api/v2/outgoing-smtp/existing",
		"name", "existing", body)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	api.config.OutgoingSMTPMu.Lock()
	updated := api.config.OutgoingSMTP["existing"]
	api.config.OutgoingSMTPMu.Unlock()

	if updated.Host != "new.host.com" {
		t.Errorf("expected host 'new.host.com', got '%s'", updated.Host)
	}
	if updated.Port != "25" {
		t.Errorf("expected port unchanged '25', got '%s'", updated.Port)
	}
	if updated.Email != "old@test.com" {
		t.Errorf("expected email unchanged 'old@test.com', got '%s'", updated.Email)
	}
}

func TestUpdateOutgoingSMTP_MultipleFields(t *testing.T) {
	api := newTestAPIv2()
	api.config.OutgoingSMTP["multi"] = &config.OutgoingSMTP{
		Name: "multi", Host: "old.host.com", Port: "25",
	}

	body := config.OutgoingSMTP{Host: "new.host.com", Port: "587", Email: "new@test.com"}
	rr := doRequestWithParam(api.updateOutgoingSMTP, "PUT", "/api/v2/outgoing-smtp/multi",
		"name", "multi", body)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	api.config.OutgoingSMTPMu.Lock()
	updated := api.config.OutgoingSMTP["multi"]
	api.config.OutgoingSMTPMu.Unlock()

	if updated.Host != "new.host.com" {
		t.Errorf("expected host 'new.host.com', got '%s'", updated.Host)
	}
	if updated.Port != "587" {
		t.Errorf("expected port '587', got '%s'", updated.Port)
	}
	if updated.Email != "new@test.com" {
		t.Errorf("expected email 'new@test.com', got '%s'", updated.Email)
	}
}

func TestUpdateOutgoingSMTP_NotFound(t *testing.T) {
	api := newTestAPIv2()
	body := config.OutgoingSMTP{Host: "new.host.com"}
	rr := doRequestWithParam(api.updateOutgoingSMTP, "PUT", "/api/v2/outgoing-smtp/nonexistent",
		"name", "nonexistent", body)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestUpdateOutgoingSMTP_InvalidMechanism(t *testing.T) {
	api := newTestAPIv2()
	api.config.OutgoingSMTP["existing"] = &config.OutgoingSMTP{
		Name: "existing", Host: "old.host.com", Port: "25",
	}
	body := config.OutgoingSMTP{Mechanism: "INVALID"}
	rr := doRequestWithParam(api.updateOutgoingSMTP, "PUT", "/api/v2/outgoing-smtp/existing",
		"name", "existing", body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// --- Delete tests ---

func TestDeleteOutgoingSMTP_Existing(t *testing.T) {
	api := newTestAPIv2()
	api.config.OutgoingSMTP["to-delete"] = &config.OutgoingSMTP{
		Name: "to-delete", Host: "h", Port: "25",
	}

	rr := doRequestWithParam(api.deleteOutgoingSMTP, "DELETE", "/api/v2/outgoing-smtp/to-delete",
		"name", "to-delete", nil)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}

	api.config.OutgoingSMTPMu.Lock()
	_, exists := api.config.OutgoingSMTP["to-delete"]
	api.config.OutgoingSMTPMu.Unlock()
	if exists {
		t.Error("template should be deleted from map")
	}
}

func TestDeleteOutgoingSMTP_ThenNotInList(t *testing.T) {
	api := newTestAPIv2()
	api.config.OutgoingSMTP["keep-me"] = &config.OutgoingSMTP{
		Name: "keep-me", Host: "h", Port: "25",
	}
	api.config.OutgoingSMTP["delete-me"] = &config.OutgoingSMTP{
		Name: "delete-me", Host: "h", Port: "25",
	}

	// Delete one
	rr := doRequestWithParam(api.deleteOutgoingSMTP, "DELETE", "/api/v2/outgoing-smtp/delete-me",
		"name", "delete-me", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}

	// GET list
	listRR := doRequest(api.listOutgoingSMTP, "GET", "/api/v2/outgoing-smtp", nil)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRR.Code)
	}

	var result map[string]*config.OutgoingSMTP
	json.Unmarshal(listRR.Body.Bytes(), &result)
	if _, ok := result["keep-me"]; !ok {
		t.Error("'keep-me' should still be in list")
	}
	if _, ok := result["delete-me"]; ok {
		t.Error("'delete-me' should NOT be in list after deletion")
	}
}

func TestDeleteOutgoingSMTP_NotFound(t *testing.T) {
	api := newTestAPIv2()
	rr := doRequestWithParam(api.deleteOutgoingSMTP, "DELETE", "/api/v2/outgoing-smtp/ghost",
		"name", "ghost", nil)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// --- List tests ---

func TestListOutgoingSMTP_PreloadedConfig(t *testing.T) {
	api := newTestAPIv2()
	// Simulate startup loading from JSON file
	api.config.OutgoingSMTP["startup-template"] = &config.OutgoingSMTP{
		Name: "startup-template", Host: "pre.loaded.com", Port: "465",
		Email: "pre@test.com", Mechanism: "PLAIN",
	}
	api.config.OutgoingSMTP["second-template"] = &config.OutgoingSMTP{
		Name: "second-template", Host: "second.com", Port: "587",
	}

	rr := doRequest(api.listOutgoingSMTP, "GET", "/api/v2/outgoing-smtp", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var result map[string]*config.OutgoingSMTP
	json.Unmarshal(rr.Body.Bytes(), &result)

	if len(result) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(result))
	}
	tmpl, ok := result["startup-template"]
	if !ok {
		t.Fatal("expected 'startup-template' in result")
	}
	if tmpl.Host != "pre.loaded.com" {
		t.Errorf("expected host 'pre.loaded.com', got '%s'", tmpl.Host)
	}
	if tmpl.Port != "465" {
		t.Errorf("expected port '465', got '%s'", tmpl.Port)
	}
	if tmpl.Email != "pre@test.com" {
		t.Errorf("expected email 'pre@test.com', got '%s'", tmpl.Email)
	}
	if _, ok := result["second-template"]; !ok {
		t.Error("expected 'second-template' in result")
	}
}

func TestListOutgoingSMTP_EmptyConfig(t *testing.T) {
	api := newTestAPIv2()
	rr := doRequest(api.listOutgoingSMTP, "GET", "/api/v2/outgoing-smtp", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var result map[string]*config.OutgoingSMTP
	json.Unmarshal(rr.Body.Bytes(), &result)
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}
}

// --- Release tests ---

func TestReleaseMessage_NonExistentTemplate(t *testing.T) {
	api := newTestAPIv2()
	storedID := storeTestMessage(api.config.Storage)

	body := ReleaseRequest{Name: "does-not-exist"}
	rr := doRequestWithParam(api.releaseMessage, "POST",
		"/api/v2/messages/"+storedID+"/release",
		"id", storedID, body)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
	var errResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &errResp)
	if !strings.Contains(errResp["error"], "template") {
		t.Errorf("error should mention 'template', got: %s", errResp["error"])
	}
}

func TestReleaseMessage_MissingHostPort(t *testing.T) {
	api := newTestAPIv2()
	storedID := storeTestMessage(api.config.Storage)

	body := ReleaseRequest{Email: "test@test.com"}
	rr := doRequestWithParam(api.releaseMessage, "POST",
		"/api/v2/messages/"+storedID+"/release",
		"id", storedID, body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var errResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &errResp)
	if !strings.Contains(errResp["error"], "host and port") {
		t.Errorf("error should mention 'host and port', got: %s", errResp["error"])
	}
}

func TestReleaseMessage_MissingEmail(t *testing.T) {
	api := newTestAPIv2()
	storedID := storeTestMessage(api.config.Storage)

	body := ReleaseRequest{Host: "smtp.test.com", Port: "587"}
	rr := doRequestWithParam(api.releaseMessage, "POST",
		"/api/v2/messages/"+storedID+"/release",
		"id", storedID, body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var errResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &errResp)
	if !strings.Contains(errResp["error"], "email") {
		t.Errorf("error should mention 'email', got: %s", errResp["error"])
	}
}

func TestReleaseMessage_NonExistentMessage(t *testing.T) {
	api := newTestAPIv2()

	body := ReleaseRequest{Host: "smtp.test.com", Port: "587", Email: "test@test.com"}
	rr := doRequestWithParam(api.releaseMessage, "POST",
		"/api/v2/messages/nonexistent/release",
		"id", "nonexistent", body)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestReleaseMessage_TemplateWithEmailOverride(t *testing.T) {
	api := newTestAPIv2()

	// Pre-populate template
	api.config.OutgoingSMTP["tmpl"] = &config.OutgoingSMTP{
		Name: "tmpl", Host: "smtp.test.com", Port: "587",
		Email: "original@test.com", Mechanism: "PLAIN",
	}

	storedID := storeTestMessage(api.config.Storage)

	// smtp.SendMail will fail (no real server), but template resolution and
	// email override succeed — error comes from SMTP send (500), not validation (400/404).
	body := ReleaseRequest{
		Name:  "tmpl",
		Email: "override@test.com",
	}
	rr := doRequestWithParam(api.releaseMessage, "POST",
		"/api/v2/messages/"+storedID+"/release",
		"id", storedID, body)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 (SMTP send failure), got %d: %s", rr.Code, rr.Body.String())
	}
	var errResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &errResp)
	if !strings.Contains(errResp["error"], "release failed") {
		t.Errorf("error should indicate release failed, got: %s", errResp["error"])
	}
}

func TestReleaseMessage_TemplateWithoutOverride(t *testing.T) {
	api := newTestAPIv2()

	api.config.OutgoingSMTP["tmpl2"] = &config.OutgoingSMTP{
		Name: "tmpl2", Host: "smtp.test.com", Port: "587",
		Email: "default@test.com",
	}

	storedID := storeTestMessage(api.config.Storage)

	// No email override — should use template's email
	body := ReleaseRequest{Name: "tmpl2"}
	rr := doRequestWithParam(api.releaseMessage, "POST",
		"/api/v2/messages/"+storedID+"/release",
		"id", storedID, body)

	// 500 from SMTP send failure, not 400/404 validation error
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 (SMTP send failure), got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestReleaseMessage_InvalidMechanism(t *testing.T) {
	api := newTestAPIv2()
	storedID := storeTestMessage(api.config.Storage)

	body := ReleaseRequest{
		Host: "smtp.test.com", Port: "587",
		Email: "test@test.com", Mechanism: "INVALID",
	}
	rr := doRequestWithParam(api.releaseMessage, "POST",
		"/api/v2/messages/"+storedID+"/release",
		"id", storedID, body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestReleaseMessage_InvalidJSON(t *testing.T) {
	api := newTestAPIv2()
	storedID := storeTestMessage(api.config.Storage)

	req := httptest.NewRequest("POST", "/api/v2/messages/"+storedID+"/release",
		bytes.NewBufferString("{bad json"))
	req.URL.RawQuery = ":id=" + storedID
	rr := httptest.NewRecorder()
	http.HandlerFunc(api.releaseMessage).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// --- Integration lifecycle test ---

func TestCRUDLifecycle(t *testing.T) {
	api := newTestAPIv2()

	// 1. Create
	createBody := config.OutgoingSMTP{
		Name: "lifecycle", Host: "smtp.example.com", Port: "587", Email: "a@b.com",
	}
	rr := doRequest(api.createOutgoingSMTP, "POST", "/api/v2/outgoing-smtp", createBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", rr.Code)
	}

	// 2. List — should contain the created template
	listRR := doRequest(api.listOutgoingSMTP, "GET", "/api/v2/outgoing-smtp", nil)
	var listResult map[string]*config.OutgoingSMTP
	json.Unmarshal(listRR.Body.Bytes(), &listResult)
	if _, ok := listResult["lifecycle"]; !ok {
		t.Fatal("list: expected 'lifecycle' in result")
	}
	if listResult["lifecycle"].Host != "smtp.example.com" {
		t.Errorf("list: expected host 'smtp.example.com', got '%s'", listResult["lifecycle"].Host)
	}

	// 3. Update — change host and port
	updateBody := config.OutgoingSMTP{Host: "new.smtp.com", Port: "465"}
	updateRR := doRequestWithParam(api.updateOutgoingSMTP, "PUT",
		"/api/v2/outgoing-smtp/lifecycle", "name", "lifecycle", updateBody)
	if updateRR.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d", updateRR.Code)
	}

	// 4. List again — verify update took effect
	listRR2 := doRequest(api.listOutgoingSMTP, "GET", "/api/v2/outgoing-smtp", nil)
	var listResult2 map[string]*config.OutgoingSMTP
	json.Unmarshal(listRR2.Body.Bytes(), &listResult2)
	if listResult2["lifecycle"].Host != "new.smtp.com" {
		t.Errorf("list after update: expected host 'new.smtp.com', got '%s'", listResult2["lifecycle"].Host)
	}
	if listResult2["lifecycle"].Port != "465" {
		t.Errorf("list after update: expected port '465', got '%s'", listResult2["lifecycle"].Port)
	}
	if listResult2["lifecycle"].Email != "a@b.com" {
		t.Errorf("list after update: expected email unchanged 'a@b.com', got '%s'", listResult2["lifecycle"].Email)
	}

	// 5. Delete
	delRR := doRequestWithParam(api.deleteOutgoingSMTP, "DELETE",
		"/api/v2/outgoing-smtp/lifecycle", "name", "lifecycle", nil)
	if delRR.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", delRR.Code)
	}

	// 6. List — should not contain the deleted template
	listRR3 := doRequest(api.listOutgoingSMTP, "GET", "/api/v2/outgoing-smtp", nil)
	var listResult3 map[string]*config.OutgoingSMTP
	json.Unmarshal(listRR3.Body.Bytes(), &listResult3)
	if _, ok := listResult3["lifecycle"]; ok {
		t.Error("list after delete: 'lifecycle' should not be in result")
	}

	// 7. Attempt to delete again — should 404
	delRR2 := doRequestWithParam(api.deleteOutgoingSMTP, "DELETE",
		"/api/v2/outgoing-smtp/lifecycle", "name", "lifecycle", nil)
	if delRR2.Code != http.StatusNotFound {
		t.Fatalf("delete again: expected 404, got %d", delRR2.Code)
	}

	// 8. Attempt to update again — should 404
	updateRR2 := doRequestWithParam(api.updateOutgoingSMTP, "PUT",
		"/api/v2/outgoing-smtp/lifecycle", "name", "lifecycle",
		config.OutgoingSMTP{Host: "x"})
	if updateRR2.Code != http.StatusNotFound {
		t.Fatalf("update after delete: expected 404, got %d", updateRR2.Code)
	}
}
