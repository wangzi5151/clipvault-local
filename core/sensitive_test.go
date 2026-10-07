package main

import "testing"

func TestIsSensitiveHits(t *testing.T) {
	hits := []string{
		"我的密码是123456",
		"请输入口令",
		"验证码：8842",
		"微信支付密码",
		"银行账号 6222",
		"信用卡还款",
		"api token=abc",
		"set passwd for user",
		"sudo pwd",
	}
	for _, s := range hits {
		if !IsSensitive(s) {
			t.Errorf("expected sensitive: %q", s)
		}
	}
}

func TestIsSensitiveCaseInsensitive(t *testing.T) {
	for _, s := range []string{"TOKEN", "Token", "PASSWD", "PassWd", "PWD", "pWd"} {
		if !IsSensitive("prefix " + s + " suffix") {
			t.Errorf("expected case-insensitive hit: %q", s)
		}
	}
}

func TestIsSensitiveMisses(t *testing.T) {
	misses := []string{
		"",
		"hello world",
		"13800138000 北京市朝阳区",
		"订单号 20261007-8848",
		"passwords are not here", // 'password' alone is not a keyword
		"tokens",                 // plural 'tokens' contains 'token' -> HIT actually
	}
	for _, s := range misses[:5] {
		if IsSensitive(s) {
			t.Errorf("expected not sensitive: %q", s)
		}
	}
	// 'tokens' contains substring 'token' -> sensitive (documented behavior)
	if !IsSensitive(misses[5]) {
		t.Errorf("expected substring hit: %q", misses[5])
	}
}

func TestSensitivePreviewTruncates(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "密"
	}
	p := SensitivePreview(long)
	if len([]rune(p)) != 60 {
		t.Errorf("preview should be 60 runes, got %d", len([]rune(p)))
	}
}
