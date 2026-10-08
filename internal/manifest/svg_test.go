package manifest

import (
	"bytes"
	"strings"
	"testing"
)

func TestValidateSVGStaticArtwork(t *testing.T) {
	valid := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">
  <defs><linearGradient id="paint"><stop offset="0" stop-color="#fff"/></linearGradient></defs>
  <path fill="url(#paint)" d="M1 1h30v30H1z"/>
  <title>Fixture icon</title>
</svg>`)
	if err := ValidateSVG(valid); err != nil {
		t.Fatalf("valid static SVG rejected: %v", err)
	}
	if got, err := ValidateRemoteDesktopIcon(valid, ".SVG"); err != nil || got != 0 {
		t.Fatalf("remote SVG validation = %d, %v", got, err)
	}
}

func TestValidateSVGRejectsMalformedAndWrongRoot(t *testing.T) {
	for name, data := range map[string][]byte{
		"not XML":             []byte("this is not XML"),
		"malformed XML":       []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path></svg>`),
		"wrong root":          []byte(`<html xmlns="http://www.w3.org/1999/xhtml"/>`),
		"wrong namespace":     []byte(`<svg xmlns="urn:wrong"/>`),
		"multiple roots":      []byte(`<svg xmlns="http://www.w3.org/2000/svg"/><svg xmlns="http://www.w3.org/2000/svg"/>`),
		"invalid declaration": []byte(`<?xml garbage?><svg xmlns="http://www.w3.org/2000/svg"/>`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSVG(data); err == nil {
				t.Fatal("invalid SVG accepted")
			}
		})
	}
}

func TestValidateSVGRejectsActiveAndExternalContent(t *testing.T) {
	for name, body := range map[string]string{
		"script":              `<script>alert(1)</script>`,
		"event handler":       `<path onload="alert(1)"/>`,
		"external image":      `<image href="https://example.invalid/icon.png"/>`,
		"external use":        `<use href="//example.invalid/icon.svg#id"/>`,
		"external stylesheet": `<style>@import url(https://example.invalid/a.css);</style>`,
		"foreign object":      `<foreignObject><div>active</div></foreignObject>`,
		"embedded content":    `<object data="data:text/html,evil"/>`,
		"external paint":      `<path fill="url(https://example.invalid/a.svg#paint)"/>`,
		"unknown element":     `<animate attributeName="x"/>`,
		"XML stylesheet PI":   `<?xml-stylesheet href="https://example.invalid/a.css"?>`,
		"DOCTYPE":             `<!DOCTYPE svg [<!ENTITY x "boom">]>`,
		"entity reference":    `<title>&external;</title>`,
	} {
		t.Run(name, func(t *testing.T) {
			data := []byte(`<svg xmlns="http://www.w3.org/2000/svg">` + body + `</svg>`)
			if err := ValidateSVG(data); err == nil {
				t.Fatal("unsafe SVG accepted")
			}
		})
	}
}

func TestValidateSVGRejectsResourceExhaustionAndWrongFormat(t *testing.T) {
	if err := ValidateSVG(bytes.Repeat([]byte(" "), maxSVGBytes+1)); err == nil {
		t.Fatal("oversized SVG accepted")
	}
	deep := `<svg xmlns="http://www.w3.org/2000/svg">` + strings.Repeat("<g>", maxSVGDepth+1) + strings.Repeat("</g>", maxSVGDepth+1) + `</svg>`
	if err := ValidateSVG([]byte(deep)); err == nil {
		t.Fatal("excessively nested SVG accepted")
	}
	largePath := `<svg xmlns="http://www.w3.org/2000/svg"><path d="` + strings.Repeat("M1 1 ", maxSVGAttrSize/5+1) + `"/></svg>`
	if err := ValidateSVG([]byte(largePath)); err == nil {
		t.Fatal("oversized path attribute accepted")
	}
	if _, err := ValidateRemoteDesktopIcon([]byte("<svg/>"), ".png"); err == nil {
		t.Fatal("SVG bytes accepted under a PNG extension")
	}
	if _, err := ValidateRemoteDesktopIcon([]byte("not svg"), ".svg"); err == nil {
		t.Fatal("non-SVG bytes accepted under an SVG extension")
	}
	if _, err := ValidateRemoteDesktopIcon([]byte("icon"), ".ico"); err == nil {
		t.Fatal("unsupported remote icon extension accepted")
	}
}
