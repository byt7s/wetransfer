package api

import (
	"encoding/base64"
	"testing"
)

func token(payload string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

func TestParseStormToken(t *testing.T) {
	claims, err := ParseStormToken(token(`{
		"storm.preflight_batch_url":"https://storm.test/preflight",
		"storm.announce_blocks_url":"https://storm.test/blocks",
		"storm.create_batch_url":"https://storm.test/batch",
		"storm.foreign_id":"abc"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if claims.PreflightURL != "https://storm.test/preflight" {
		t.Errorf("preflight = %q", claims.PreflightURL)
	}
	if claims.ForeignID != "abc" {
		t.Errorf("foreign id = %q", claims.ForeignID)
	}
}

func TestParseStormTokenErrors(t *testing.T) {
	for _, tok := range []string{
		"",
		"one.two",
		"e30.bm90LWpzb24.sig",
		token(`{"storm.preflight_batch_url":"https://x"}`),
	} {
		if _, err := ParseStormToken(tok); err == nil {
			t.Errorf("ParseStormToken(%q) should fail", tok)
		}
	}
}
