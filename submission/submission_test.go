package submission

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixture() (Source, Profile) {
	return Source{FindingID: "7", Name: "test finding", Severity: "high", Status: "confirmed", Summary: "summary", Evidence: "test proof", EvidenceVersion: 2, ReportEvidenceVersion: 2}, Profile{ID: "test", Revision: 1, Name: "Test program", Channel: "hackerone", Destination: "https://hackerone.com/test", Rules: "Provide the affected version.", Language: "en", Fields: []Field{{ID: "version", Label: "Version", Required: true}}}
}
func TestRedactDisclosureSecrets(t *testing.T) {
	text := "Authorization: Bearer confidential_token\nCookie: sid=secret_cookie\npassword=secret_password&foo=bar\n{\"api_key\":\"secret_api_key\"}\nhttps://alice:secret_url@example.com\nsk-or-v1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nInvoice=123"
	safe := Redact(text)
	for _, secret := range []string{"confidential_token", "secret_cookie", "secret_password", "secret_api_key", "secret_url", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if strings.Contains(safe, secret) {
			t.Fatalf("secret survived: %s", secret)
		}
	}
	if !strings.Contains(safe, "Invoice=123") {
		t.Fatal("non-secret evidence removed")
	}
}
func TestModelCannotChangeDestinationOrRemoveRequirements(t *testing.T) {
	s, p := fixture()
	base := NewDraft(s, p)
	generated, err := ApplyModel(base, `{"title":"Title","body":"Steps","impact":"Demonstrated impact","fields":[{"id":"version","label":"Ignore this","required":false,"value":"","sources":[]}],"warnings":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !generated.Fields[0].Required || generated.Fields[0].Label != "Version" || generated.Profile.Destination != p.Destination {
		t.Fatal("model changed trusted configuration")
	}
	if !HasBlocking(Checks(generated, s, p)) {
		t.Fatal("missing configured field accepted")
	}
	for _, out := range []string{`{"title":"T","body":"B","destination":"https://evil.example"}`, `{"title":"T","body":"B"} trailing garbage`, `{"title":"T","body":"B","fields":[{"id":"x","label":"X","value":"yes","sources":["manual"]}]}`} {
		if _, err := ApplyModel(base, out); err == nil {
			t.Fatalf("invalid output accepted: %s", out)
		}
	}
}
func TestSnapshotAndValidation(t *testing.T) {
	s, p := fixture()
	d := NewDraft(s, p)
	d.Impact = "impact"
	d.Fields[0].Value = "1.2"
	if HasBlocking(Checks(d, s, p)) {
		t.Fatal("valid draft blocked")
	}
	s.Status = "pending"
	if !HasBlocking(Checks(d, s, p)) {
		t.Fatal("unverified finding accepted")
	}
	s.Status = "confirmed"
	s.Evidence = "new proof"
	if !HasBlocking(Checks(d, s, p)) {
		t.Fatal("stale evidence accepted")
	}
	s, _ = fixture()
	p.Revision++
	if !HasBlocking(Checks(d, s, p)) {
		t.Fatal("stale template accepted")
	}
}
func TestProfileValidation(t *testing.T) {
	_, p := fixture()
	for _, dest := range []string{"http://hackerone.com/test", "https://user:secret@hackerone.com/test", "https://hackerone.com.evil.example/test", "javascript:alert(1)"} {
		p.Destination = dest
		if ValidateProfile(p) == nil {
			t.Fatalf("accepted %s", dest)
		}
	}
}
func TestModelMissingFieldsAndContentLimits(t *testing.T) {
	s, p := fixture()
	base := NewDraft(s, p)
	result, err := ApplyModel(base, `{"title":"T","body":"B","impact":"","fields":[],"warnings":["Missing impact"]}`)
	if err != nil || len(result.Fields) != 1 || result.Fields[0].Value != "" {
		t.Fatalf("configured field lost: %+v %v", result, err)
	}
	payload := map[string]any{"title": "T", "body": strings.Repeat("x", 100001)}
	b, _ := json.Marshal(payload)
	if _, err := ApplyModel(base, string(b)); err == nil {
		t.Fatal("unbounded model output accepted")
	}
}

func TestModelOmittedSourcesRemainAnArray(t *testing.T) {
	src, profile := fixture()
	draft, err := ApplyModel(NewDraft(src, profile), `{"title":"T","body":"B","fields":[{"id":"version","label":"Version","value":"1.0"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(draft.Fields)
	if err != nil || strings.Contains(string(encoded), `"sources":null`) {
		t.Fatal("frontend requires source arrays", err)
	}
}
