package main

import (
	ua "github.com/mileusna/useragent"
)

// classifyUA derives the device/OS/browser analytics dimensions from the
// request User-Agent. These are data-quality fields, not trust-bearing: the
// UA is client-claimed by nature, and the raw string stays on the row for
// re-derivation.
func classifyUA(raw string) (deviceClass, osFamily, browserFamily string) {
	if raw == "" {
		return "", "", ""
	}
	p := ua.Parse(raw)
	switch {
	case p.Bot:
		deviceClass = "bot"
	case p.Mobile:
		deviceClass = "mobile"
	case p.Tablet:
		deviceClass = "tablet"
	case p.Desktop:
		deviceClass = "desktop"
	}
	return deviceClass, truncate(p.OS, 64), truncate(p.Name, 64)
}
