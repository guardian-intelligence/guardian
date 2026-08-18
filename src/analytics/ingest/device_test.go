package main

import "testing"

func TestClassifyUA(t *testing.T) {
	cases := []struct {
		name    string
		ua      string
		device  string
		os      string
		browser string
	}{
		{
			name:    "iphone safari",
			ua:      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
			device:  "mobile",
			os:      "iOS",
			browser: "Safari",
		},
		{
			name:    "windows chrome",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			device:  "desktop",
			os:      "Windows",
			browser: "Chrome",
		},
		{
			name:    "android chrome",
			ua:      "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36",
			device:  "mobile",
			os:      "Android",
			browser: "Chrome",
		},
		{
			name:   "googlebot wins over desktop",
			ua:     "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			device: "bot",
		},
		{name: "empty", ua: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			device, osf, browser := classifyUA(c.ua)
			if device != c.device {
				t.Errorf("device = %q, want %q", device, c.device)
			}
			if c.os != "" && osf != c.os {
				t.Errorf("os = %q, want %q", osf, c.os)
			}
			if c.browser != "" && browser != c.browser {
				t.Errorf("browser = %q, want %q", browser, c.browser)
			}
		})
	}
}
