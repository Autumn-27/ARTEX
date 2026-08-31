package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestCompanyScopeInputsAcceptStructuredAndLegacyRules(t *testing.T) {
	var inputs companyScopeInputs
	if err := json.Unmarshal([]byte(`[
		"example.com",
		{"kind":"icp","value":"京 ICP备 123号"},
		{"kind":"keyword","value":"Acme Security"}
	]`), &inputs); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 3 {
		t.Fatalf("input count=%d want 3", len(inputs))
	}
	if inputs[0].Kind != "" || inputs[0].Value != "example.com" {
		t.Fatalf("legacy rule changed: %+v", inputs[0])
	}
	if inputs[1].Kind != "icp" || inputs[2].Kind != "keyword" {
		t.Fatalf("structured rules changed: %+v", inputs)
	}
}

func TestCreateCompanyRejectsNormalizedDuplicateWithoutChangingScope(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()

	stamp := time.Now().UnixNano()
	name := fmt.Sprintf("HTTP Strict Company %d", stamp)
	companyID, _, _, _, _, err := m.pg.Companies().CreateCompanyWithScope(name, "", []db.ScopeInput{
		{Kind: "domain", Value: fmt.Sprintf("existing-%d.example", stamp)},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.Companies().DeleteCompany(companyID) }()

	s := &Server{m: m}
	body, err := json.Marshal(map[string]any{
		"name":  "  " + name + "  ",
		"scope": []map[string]string{{"kind": "domain", "value": fmt.Sprintf("new-%d.example", stamp)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/companies", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.createCompany(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create status=%d body=%s", rec.Code, rec.Body.String())
	}
	scope, err := m.pg.Companies().GetScope(companyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 1 || scope[0].Domain != fmt.Sprintf("existing-%d.example", stamp) {
		t.Fatalf("duplicate HTTP create changed existing scope: %+v", scope)
	}
}

func TestDeleteCompanyRefreshesLiveTaskCompanyIDs(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()

	companyID, _, err := m.pg.Companies().UpsertCompany(
		fmt.Sprintf("Delete Company DTO %d", time.Now().UnixNano()), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTaskWithOptions("company deletion dto", "goal", db.TaskCreateOptions{
		CompanyIDs: []int64{companyID},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()

	s := &Server{m: m}
	req := httptest.NewRequest(http.MethodDelete, "/api/companies/1", bytes.NewBufferString(`{}`))
	req.SetPathValue("id", i64s(companyID))
	rec := httptest.NewRecorder()
	s.deleteCompany(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete company status=%d body=%s", rec.Code, rec.Body.String())
	}
	if dto := taskDTO(task, "created"); len(dto.CompanyIDs) != 0 {
		t.Fatalf("live task DTO retained deleted company: %+v", dto.CompanyIDs)
	}
	persisted, err := m.pg.GetTask(mustTaskID(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || len(persisted.CompanyIDs) != 0 {
		t.Fatalf("persisted task retained deleted company: %+v", persisted)
	}
}

func TestCompanyScopeHTTPErrorClassificationAndBounds(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := &Server{m: m}

	t.Run("missing company is 404", func(t *testing.T) {
		body := bytes.NewBufferString(`{"scope":[]}`)
		req := httptest.NewRequest(http.MethodPost, "/api/companies/999999999/scope", body)
		req.SetPathValue("id", "999999999")
		rec := httptest.NewRecorder()
		s.addCompanyScope(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("oversized Unicode scope is 400", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"name": "Oversized Scope",
			"scope": []map[string]string{{
				"kind": "keyword", "value": strings.Repeat("界", db.MaxCompanyScopeRawRunes+1),
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/companies", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.createCompany(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("scope rule count is unbounded", func(t *testing.T) {
		scope := make([]map[string]string, 300)
		for i := range scope {
			scope[i] = map[string]string{"kind": "keyword", "value": fmt.Sprintf("unbounded-%d-%d", time.Now().UnixNano(), i)}
		}
		body, err := json.Marshal(map[string]any{"name": fmt.Sprintf("Unbounded Scope %d", time.Now().UnixNano()), "scope": scope})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/companies", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.createCompany(rec, req)
		var created struct {
			ID    int64 `json:"id"`
			Added int   `json:"scope_added"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &created)
		defer func() { _ = m.pg.Companies().DeleteCompany(created.ID) }()
		if rec.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if created.Added != len(scope) {
			t.Fatalf("scope_added=%d want=%d", created.Added, len(scope))
		}
	})

	t.Run("oversized request body is 413", func(t *testing.T) {
		body := `{"name":"` + strings.Repeat("x", maxCompanyMutationBodyBytes+1) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/companies", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.createCompany(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestListAssetsClassifiesValidationAndDatabaseErrors(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := &Server{m: m}

	req := httptest.NewRequest(http.MethodGet, "/api/assets?dsl=(", nil)
	rec := httptest.NewRecorder()
	s.listAssets(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid DSL status=%d body=%s", rec.Code, rec.Body.String())
	}

	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/assets?type=root_domain", nil)
	rec = httptest.NewRecorder()
	s.listAssets(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("database failure status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteCompanyRejectsBadJSONAndReportsMissing(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := &Server{m: m}

	companyID, _, err := m.pg.Companies().UpsertCompany(
		fmt.Sprintf("Delete JSON Company %d", time.Now().UnixNano()), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.Companies().DeleteCompany(companyID) }()

	for _, body := range []string{`{"delete_assets":`, `{}` + `{}`} {
		req := httptest.NewRequest(http.MethodDelete, "/api/companies/1", strings.NewReader(body))
		req.SetPathValue("id", i64s(companyID))
		rec := httptest.NewRecorder()
		s.deleteCompany(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%q status=%d response=%s", body, rec.Code, rec.Body.String())
		}
		company, err := m.pg.Companies().GetCompany(companyID)
		if err != nil || company == nil {
			t.Fatalf("bad JSON deleted company: company=%v err=%v", company, err)
		}
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/companies/999999999", nil)
	req.SetPathValue("id", "999999999")
	rec := httptest.NewRecorder()
	s.deleteCompany(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing delete status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCompanyScopeSystemFailureIsHTTP500(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := &Server{m: m}
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/companies/1/scope", bytes.NewBufferString(`{"scope":[]}`))
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	s.addCompanyScope(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
