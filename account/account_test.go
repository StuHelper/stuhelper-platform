package account_test

import (
	"encoding/base64"
	"github.com/StuHelper/stuhelper-platform/account"
	"strings"
	"testing"
)

func TestAccountKeyRejectsAmbiguousMalformedAndUnboundedIdentities(t *testing.T) {
	p := account.Principal{Issuer: "https://issuer.example/甲", Subject: "user:乙"}
	if actual, err := account.Parse(p.Key()); err != nil || actual != p {
		t.Fatal(actual, err)
	}
	for _, key := range []string{"", ":", "YQ:Yg:", "YQ=:Yg", "YR:Yg", "YQ:\nYg", "YQ:" + base64.RawURLEncoding.EncodeToString([]byte{0xff}), "YQ:" + base64.RawURLEncoding.EncodeToString([]byte("user\x00")), strings.Repeat("a", 2049)} {
		if _, err := account.Parse(key); err == nil {
			t.Fatal("invalid key accepted", key)
		}
	}
	if (account.Principal{Issuer: "issuer", Subject: "\n"}).Key() != "" {
		t.Fatal("invalid principal encoded")
	}
}
