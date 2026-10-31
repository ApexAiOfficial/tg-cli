package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

// signedURL builds a web view URL the way Telegram does: launch data in the fragment.
func signedURL(t *testing.T, page string) string {
	t.Helper()
	data := url.Values{
		"query_id":    {"AAH1"},
		"user":        {`{"id":42,"first_name":"Test","language_code":"en"}`},
		"auth_date":   {"1793400000"},
		"start_param": {"promo"},
		"hash":        {strings.Repeat("a", 64)},
	}
	fragment := url.Values{
		"tgWebAppData":     {data.Encode()},
		"tgWebAppVersion":  {"9.1"},
		"tgWebAppPlatform": {"tdesktop"},
	}
	return page + "#" + fragment.Encode()
}

func TestParseWebAppURL(t *testing.T) {
	launch, err := parseWebAppURL(signedURL(t, "https://store.example:8443/app?x=1"))
	if err != nil {
		t.Fatal(err)
	}
	if launch.Page != "https://store.example:8443/app?x=1" {
		t.Errorf("page keeps its fragment: %q", launch.Page)
	}
	if launch.Version != "9.1" || launch.Platform != "tdesktop" {
		t.Errorf("version %q platform %q", launch.Version, launch.Platform)
	}
	if launch.UserID != 42 || launch.AuthDate != 1793400000 {
		t.Errorf("user %d auth date %d", launch.UserID, launch.AuthDate)
	}
	if launch.QueryID != "AAH1" || launch.StartParam != "promo" {
		t.Errorf("query id %q start param %q", launch.QueryID, launch.StartParam)
	}
	if !strings.Contains(launch.InitData, "hash=") {
		t.Errorf("init data lost its hash: %q", launch.InitData)
	}
}

func TestParseWebAppURLWithoutLaunchData(t *testing.T) {
	launch, err := parseWebAppURL("https://store.example/")
	if err != nil {
		t.Fatal(err)
	}
	if launch.Page != "https://store.example/" || launch.InitData != "" {
		t.Errorf("got %+v", launch)
	}
}

func TestParseWebAppURLErrorsNeverQuoteTheCredential(t *testing.T) {
	secret := "hash=" + strings.Repeat("b", 64)
	for _, raw := range []string{
		"https://store.example/%zz#tgWebAppData=" + secret,
		"https://store.example/#tgWebAppData=%zz&" + secret,
	} {
		_, err := parseWebAppURL(raw)
		if err == nil {
			t.Fatalf("no error for %q", raw)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error quotes the credential: %v", err)
		}
	}
}

func TestWebAppResultTextHidesTheCredential(t *testing.T) {
	launch, err := parseWebAppURL(signedURL(t, "https://store.example/"))
	if err != nil {
		t.Fatal(err)
	}
	res := webAppResult{
		Peer: peerRef{ID: 7, Username: "mybot"}, Source: "menu_button", URL: launch.Page,
		Signed: true, UserID: launch.UserID, AuthDate: launch.AuthDate,
	}
	var buf bytes.Buffer
	if err := res.MarshalText(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "hash") || !strings.Contains(buf.String(), "user 42") {
		t.Errorf("text output: %q", buf.String())
	}
	encoded, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "signed_url") || strings.Contains(string(encoded), "init_data") {
		t.Errorf("JSON carries the credential without --reveal: %s", encoded)
	}
}

func TestWriteWebAppSecretIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	raw := signedURL(t, "https://store.example/")
	launch, err := parseWebAppURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWebAppSecret(path, raw, launch); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got webAppSecret
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.URL != raw || got.InitData != launch.InitData || got.Page != "https://store.example/" {
		t.Errorf("got %+v", got)
	}
}

func TestWebAppButtonURL(t *testing.T) {
	if target, simple, ok := webAppButtonURL(&tg.KeyboardButtonWebView{URL: "https://a.example/"}); !ok || simple || target != "https://a.example/" {
		t.Errorf("web view: %q %v %v", target, simple, ok)
	}
	if target, simple, ok := webAppButtonURL(&tg.KeyboardButtonSimpleWebView{URL: "https://b.example/"}); !ok || !simple || target != "https://b.example/" {
		t.Errorf("simple web view: %q %v %v", target, simple, ok)
	}
	if _, _, ok := webAppButtonURL(&tg.KeyboardButtonCallback{Data: []byte("x")}); ok {
		t.Error("a callback button is not a Mini App")
	}
}
